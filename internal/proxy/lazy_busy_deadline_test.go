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

type delayedBusyResponse struct {
	simulatedCloud
	calls atomic.Int32
}

func (tr *delayedBusyResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path != "/v1/my-device/detail" {
		return tr.simulatedCloud.RoundTrip(r)
	}
	if tr.calls.Add(1) == 1 {
		select {
		case <-time.After(20 * time.Second):
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":4,"ssid":"synthetic-wifi"}}`))}, nil
}

func TestFirstRefusalCannotExtendClientBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &delayedBusyResponse{}
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
		_, dialErr := s.DialPort(context.Background(), 4700)
		elapsed := time.Since(begin)
		closeErr := s.Close()
		t.Logf("first response delay=20s calls=%d elapsed=%v dial=%v close=%v", tr.calls.Load(), elapsed, dialErr, closeErr)
		if !errors.Is(dialErr, session.ErrBusy) || elapsed != 90*time.Second {
			t.Fatalf("caller held %v, want busy rejection at original 90s deadline: %v", elapsed, dialErr)
		}
	})
}
