package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// Literal partial replies keep a decodable conId, so each failed acquisition
// owes a stop even when required allocation material is unusable.
func malformedAllocationReplies() []struct {
	name, body string
	status     int
} {
	return []struct {
		name, body string
		status     int
	}{
		{"review_unknown_partial", `{"code":7788,"data":{"conId":"partial"}}`, 200},
		{"missing_uid", `{"code":200,"data":{"conId":"partial","sdkLicenseKey":"fixture","username":"user","password":"pass"}}`, 200},
		{"missing_license", `{"code":200,"data":{"conId":"partial","deviceUid":"fixture","username":"user","password":"pass"}}`, 200},
		{"missing_username", `{"code":200,"data":{"conId":"partial","deviceUid":"fixture","sdkLicenseKey":"fixture","password":"pass"}}`, 200},
		{"missing_password", `{"code":200,"data":{"conId":"partial","deviceUid":"fixture","sdkLicenseKey":"fixture","username":"user"}}`, 200},
		{"null_uid", `{"code":200,"data":{"conId":"partial","deviceUid":null,"sdkLicenseKey":"fixture","username":"user","password":"pass"}}`, 200},
		{"wrong_uid_type", `{"code":200,"data":{"conId":"partial","deviceUid":42,"sdkLicenseKey":"fixture","username":"user","password":"pass"}}`, 200},
		{"missing_code", `{"data":{"conId":"partial"}}`, 200},
		{"wrong_code_type", `{"code":"200","data":{"conId":"partial"}}`, 200},
		{"rejected", `{"code":300405,"data":{"conId":"partial"}}`, 200},
		{"unauthorized", `{"code":200,"data":{"conId":"partial"}}`, 401},
		{"forbidden", `{"code":200,"data":{"conId":"partial"}}`, 403},
	}
}

type failedAllocationCleanup struct {
	body          string
	status        int
	readFailure   bool
	online, stops atomic.Int32
}

func (c *failedAllocationCleanup) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.URL.Path {
	case "/v1/device-p2p/online-link":
		c.online.Add(1)
		var body io.ReadCloser = io.NopCloser(strings.NewReader(c.body))
		if c.readFailure {
			body = io.NopCloser(io.MultiReader(strings.NewReader(c.body), allocationReadFailure{}))
		}
		return &http.Response{StatusCode: c.status, Body: body}, nil
	case "/v1/device-p2p/stop":
		c.stops.Add(1)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`))}, nil
}

type allocationReadFailure struct{}

func (allocationReadFailure) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }

func TestMalformedAllocationCleanupDoesNotReacquire(t *testing.T) {

	for _, tt := range malformedAllocationReplies() {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tr := &failedAllocationCleanup{body: tt.body, status: tt.status}
				var logs bytes.Buffer
				c, err := newExpiredAllocationFixtureClient(t, cloudhttp.Config{Transport: tr, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
				if err != nil {
					t.Fatal(err)
				}
				s, err := start(context.Background(), Config{}, func(s *Session) sessionedge.Factory {
					return &factory{s: s, cloud: c, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
				})
				if err != nil {
					c.Close()
					t.Fatal(err)
				}
				defer s.Close()
				<-time.After(time.Minute)
				synctest.Wait()
				wantStops := int32(1)
				if tt.status == 401 {
					wantStops = 0
				}
				if tr.online.Load() != 1 || tr.stops.Load() != wantStops {
					t.Errorf("online-link/stop calls=%d/%d, want 1/%d", tr.online.Load(), tr.stops.Load(), wantStops)
				}
				if err := s.Close(); !errors.Is(err, session.ErrStop) {
					t.Errorf("close after uncertain allocation=%v, want ErrStop", err)
				}
				if tt.status == 401 {
					if !strings.Contains(logs.String(), `"msg":"release_not_attempted"`) || !strings.Contains(logs.String(), `"attempted":false`) {
						t.Error("tokenless cleanup not recorded as unattempted")
					}
				} else if !strings.Contains(logs.String(), `"phase":"release"`) || !strings.Contains(logs.String(), `"reason":"timeout"`) {
					t.Errorf("failed owed release was not recorded: %s", logs.String())
				}
			})
		})
	}
}

func TestAllocationCleanupPreservesPrimaryPolicy(t *testing.T) {

	for _, readFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "malformed", true: "transport"}[readFailure], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tr := &failedAllocationCleanup{body: malformedAllocationReplies()[0].body, status: 200, readFailure: readFailure}
				c, err := newFixtureClient(t, cloudhttp.Config{Transport: tr})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				a := allocator{c, cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
				lease, err := a.Allocate(context.Background())
				if lease != nil || !errors.Is(err, session.ErrReleaseAbandoned) || !errors.Is(err, session.ErrUncertain) {
					t.Errorf("allocation readFailure=%t: lease/error=%v/%v, want abandonment with observed malformed policy", readFailure, lease, err)
				}
				if tr.online.Load() != 1 || tr.stops.Load() != 1 {
					t.Errorf("online-link/stop calls=%d/%d, want 1/1", tr.online.Load(), tr.stops.Load())
				}
			})
		})
	}
}
