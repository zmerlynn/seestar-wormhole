package proxy

import (
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type r4ProfileDeadline struct {
	r4Cloud
	profiles int
	status   int
	body     string
}

func (c *r4ProfileDeadline) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/v1/user/current-profile" {
		c.profiles++
		if c.profiles == 1 {
			return &http.Response{StatusCode: c.status, Body: io.NopCloser(io.MultiReader(strings.NewReader(c.body), r4DeadlineReader{req.Context()}))}, nil
		}
	}
	return c.r4Cloud.RoundTrip(req)
}
func TestTimedOutProfileDenialDoesNotReacquire(t *testing.T) {
	for _, row := range []struct {
		name   string
		status int
		body   string
	}{{"malformed", 200, `{"code":"invalid","data":null}`}, {"denied", 409, `{"code":0,"data":null}`}} {
		t.Run(row.name, func(t *testing.T) {
			t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
			t.Setenv("SEESTAR_PASSWORD", "fixture-password")
			synctest.Test(t, func(t *testing.T) {
				tr := &r4ProfileDeadline{r4Cloud: r4Cloud{stops: map[string]int{}}, status: row.status, body: row.body}
				client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: tr, StateDir: t.TempDir(), Timeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				s, err := start(t.Context(), Config{}, func(s *Session) sessionedge.Factory {
					return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
						trace := []string{}
						return r4Wire{newSimulatedWire(t, &trace), &tr.r4Cloud, t}
					}}
				})
				if err != nil {
					client.Close()
					t.Fatal(err)
				}
				defer s.Close()
				<-time.After(10 * time.Second)
				synctest.Wait()
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				conn, dialErr := s.DialPort(ctx, 4700)
				if conn != nil {
					conn.Close()
				}
				if tr.allocations != 0 {
					t.Errorf("profile %s + real deadline: profiles=%d allocations=%d client=%v want zero allocations", row.name, tr.profiles, tr.allocations, dialErr)
				}
			})
		})
	}
}
