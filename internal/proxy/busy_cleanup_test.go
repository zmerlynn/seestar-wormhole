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
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

type busyCleanupCloud struct {
	simulatedCloud
	opens, stops atomic.Int32
	delay        bool
	denied       bool
	once         bool
}

func (c *busyCleanupCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/v1/device-p2p/online-link" {
		n := c.opens.Add(1)
		if c.once && n > 1 {
			return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"code":300404,"data":null}`))}, nil
		}
		if c.delay {
			<-time.After(89 * time.Second)
		}
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"code":300404,"data":{"conId":"review-owned"}}`))}, nil
	}
	if r.URL.Path == "/v1/device-p2p/stop" {
		c.stops.Add(1)
		if c.delay {
			<-time.After(4 * time.Second)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200}`))}, nil
		}
		if c.denied {
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"code":401}`))}, nil
		}
		return nil, errors.New("synthetic stop I/O failure")
	}
	return c.simulatedCloud.RoundTrip(r)
}
func busyCleanupSession(t *testing.T, tr *busyCleanupCloud) *Session {
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
	return s
}
func TestBusyCleanupFailureBlocksReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &busyCleanupCloud{once: true}
		s := busyCleanupSession(t, tr)
		defer s.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := s.DialPort(ctx, 4700); done <- err }()
		synctest.Wait()
		<-time.After(20 * time.Second)
		synctest.Wait()
		cancel()
		<-done
		synctest.Wait()
		closeErr := s.Close()
		t.Logf("online-link=%d failed-stops=%d close=%v", tr.opens.Load(), tr.stops.Load(), closeErr)
		if tr.opens.Load() != 1 {
			t.Errorf("online-link attempts=%d want 1 after unconfirmed release", tr.opens.Load())
		}
		if closeErr == nil {
			t.Error("shutdown hid failed internal cloud stop; want retained real failure")
		}
	})
}
func TestBusyDeniedCleanupRetained(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &busyCleanupCloud{denied: true}
		s := busyCleanupSession(t, tr)
		defer s.Close()
		_, err := s.DialPort(context.Background(), 4700)
		if err == nil {
			t.Fatal("denied release admitted client")
		}
		<-time.After(time.Minute)
		synctest.Wait()
		if n := tr.opens.Load(); n != 1 {
			t.Fatalf("denied cleanup retried %d times", n)
		}
		if err := s.Close(); err == nil {
			t.Fatal("denied cleanup lost at shutdown")
		}
	})
}
