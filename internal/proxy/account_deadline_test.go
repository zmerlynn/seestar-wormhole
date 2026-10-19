package proxy

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
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

type r4DeadlineReader struct{ ctx context.Context }

func (b r4DeadlineReader) Read([]byte) (int, error) {
	<-b.ctx.Done()
	<-time.After(20 * time.Millisecond)
	return 0, b.ctx.Err()
}

type r4AccountDeadline struct {
	r4Cloud
	loginCalls int
	body       string
}

func (c *r4AccountDeadline) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/v1/user/login" {
		c.loginCalls++
		if c.loginCalls == 1 {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(io.MultiReader(strings.NewReader(c.body), r4DeadlineReader{req.Context()}))}, nil
		}
	}
	return c.r4Cloud.RoundTrip(req)
}
func TestTimedOutMalformedLoginDoesNotReacquire(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")
	synctest.Test(t, func(t *testing.T) {
		tr := &r4AccountDeadline{r4Cloud: r4Cloud{stops: map[string]int{}}, body: `{"code":0,"data":{"accessToken":123}}`}
		client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: tr, StateDir: t.TempDir(), Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		s, err := start(t.Context(), Config{}, func(s *Session) sessionedge.Factory {
			return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
				trace := []string{}
				p := newSimulatedWire(t, &trace)
				return r4Wire{p, &tr.r4Cloud, t}
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
		if tr.loginCalls != 1 || tr.allocations != 0 {
			t.Errorf("malformed complete login followed by actual deadline: logins=%d allocations=%d client=%v want one login/no allocation", tr.loginCalls, tr.allocations, dialErr)
		}
		t.Logf("deadline login attempts=%d allocations=%d client=%v", tr.loginCalls, tr.allocations, dialErr)
	})
}
func TestAccountDeadlineControl(t *testing.T) {
	// Pure incomplete-body deadlines must continue to recover; use the existing
	// real-edge timeout matrix as a control, and verify direct disposition here.
	if cloud.Disposition(errors.Join(cloud.ErrTransport, context.DeadlineExceeded)) != cloud.Retryable {
		t.Fatal("transport deadline not retryable")
	}
}

func TestTimedOutValidLoginRecovers(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")
	synctest.Test(t, func(t *testing.T) {
		tr := &r4AccountDeadline{r4Cloud: r4Cloud{stops: map[string]int{}}, body: `{"code":0,"data":{"accessToken":"synthetic-valid"}}`}
		c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: tr, StateDir: t.TempDir(), Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		first := c.Login(t.Context())
		synctest.Wait()
		// Allow the first worker to publish the completed body after cancellation.
		<-time.After(40 * time.Millisecond)
		synctest.Wait()
		second := c.Login(t.Context())
		if !errors.Is(first, context.DeadlineExceeded) || second != nil || tr.loginCalls != 2 {
			t.Errorf("valid body + actual deadline: first=%v second=%v logins=%d want deadline/success/2", first, second, tr.loginCalls)
		}
	})
}
