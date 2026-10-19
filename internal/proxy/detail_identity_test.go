package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

type wrongIdentityCloud struct {
	simulatedCloud
	calls      atomic.Int32
	status     int
	wrongModel bool
}

func (c *wrongIdentityCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/v1/my-device/detail" {
		c.calls.Add(1)
		body := `{"code":200,"data":{"deviceSn":"wrong-device","deviceModel":"S50 Pro","deviceStatus":4,"ssid":"synthetic-wifi"}}`
		if c.wrongModel {
			body = `{"code":200,"data":{"deviceSn":"sim","deviceModel":"wrong-model","deviceStatus":4,"ssid":"synthetic-wifi"}}`
		}
		return &http.Response{StatusCode: c.status, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	return c.simulatedCloud.RoundTrip(r)
}
func TestWrongBusyIdentityIsNotHandledRefusal(t *testing.T) {
	for _, wrongModel := range []bool{false, true} {
		for _, status := range []int{200, 400} {
			t.Run(map[bool]string{false: "serial", true: "model"}[wrongModel]+"/"+http.StatusText(status), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					tr := &wrongIdentityCloud{status: status, wrongModel: wrongModel}
					c, err := newFixtureClient(t, cloudhttp.Config{Transport: tr})
					if err != nil {
						t.Fatal(err)
					}
					var trace []string
					s, err := startWithRNG(context.Background(), Config{Idle: time.Minute}, func(s *Session) sessionedge.Factory {
						return &factory{s: s, cloud: c, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner { return newSimulatedWire(t, &trace) }}
					}, zeroRetryRNG{})
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
					begin := time.Now()
					_, err = s.DialPort(context.Background(), 4700)
					if err == nil || errors.Is(err, session.ErrBusy) {
						t.Errorf("wrong identity treated as busy: %v", err)
					}
					closeErr := s.Close()
					t.Logf("HTTP=%d mismatched-identity calls=%d elapsed=%v dial=%v close=%v", status, tr.calls.Load(), time.Since(begin), err, closeErr)
					if tr.calls.Load() != 1 {
						t.Errorf("retried wrong-device detail %d times want 1", tr.calls.Load())
					}
					if closeErr == nil {
						t.Error("wrong-device detail was retired as handled busy; want real failure at shutdown")
					}
				})
			})
		}
	}
}
