package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// Independent header/body fault combinations, with complete synthetic JSON.
// EOF is replaced by a body-read timeout, as can happen after known HTTP headers.
type readTimeout struct{}

func (readTimeout) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }

type headerFailure struct {
	status        int
	stopFails     bool
	online, stops atomic.Int32
}

func (tr *headerFailure) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.URL.Path {
	case "/v1/my-device/detail":
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":{"deviceSn":"sim","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`))}, nil
	case "/v1/device-p2p/online-link":
		tr.online.Add(1)
		b := `{"code":200,"data":{"conId":"review2-partial","deviceUid":"EXAMPLEUID0000000000","sdkLicenseKey":"fixture","username":"fixture","password":"fixture"}}`
		return &http.Response{StatusCode: tr.status, Body: io.NopCloser(io.MultiReader(strings.NewReader(b), readTimeout{}))}, nil
	case "/v1/device-p2p/stop":
		tr.stops.Add(1)
		if tr.stopFails {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":null}`))}, nil
	}
	return nil, errors.New("unexpected synthetic request")
}
func TestHeaderRejectionPrimary(t *testing.T) {

	for _, status := range []int{401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tr := &headerFailure{status: status, stopFails: true}
				c, err := newExpiredAllocationFixtureClient(t, cloudhttp.Config{Transport: tr})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				_, err = c.Acquire(context.Background(), cloudhttp.Device{Serial: "sim", Model: "S50 Pro"})
				var failed *cloudhttp.AcquireError
				if !errors.As(err, &failed) || !errors.Is(failed.Primary, cloud.ErrUnauthorized) || !errors.Is(failed.Primary, cloud.ErrTransport) || !errors.Is(err, cloudhttp.ErrCleanup) {
					t.Fatalf("primary composition=%v, want authorization+transport with separate cleanup", err)
				}
				t.Logf("primary causes: authorization=%t transport=%t deadline=%t; stop attempts=%d", errors.Is(failed.Primary, cloud.ErrUnauthorized), errors.Is(failed.Primary, cloud.ErrTransport), errors.Is(failed.Primary, context.DeadlineExceeded), tr.stops.Load())
			})
		})
	}
}
func TestHeaderRejectionPolicy(t *testing.T) {

	for _, status := range []int{200, 401, 403} {
		for _, stopFails := range []bool{false, true} {
			name := http.StatusText(status) + map[bool]string{true: "/failed-stop", false: "/accepted-stop"}[stopFails]
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					tr := &headerFailure{status: status, stopFails: stopFails}
					c, err := newExpiredAllocationFixtureClient(t, cloudhttp.Config{Transport: tr})
					if err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					a := allocator{c, cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
					_, err = a.Allocate(context.Background())
					if status == 200 {
						if !errors.Is(err, session.ErrTransport) || errors.Is(err, session.ErrUncertain) {
							t.Errorf("recoverable primary body timeout=%v, want ErrTransport", err)
						}
					} else {
						want := session.ErrUncertain
						if status == 401 {
							want = session.ErrStop
						}
						if !errors.Is(err, want) || errors.Is(err, session.ErrTransport) {
							t.Errorf("HTTP %d plus primary body timeout=%v, want %v without ErrTransport", status, err, want)
						}
					}
					wantStops := int32(1)
					if status == 401 {
						wantStops = 0
					}
					if errors.Is(err, session.ErrReleaseAbandoned) != (stopFails || status == 401) || tr.stops.Load() != wantStops {
						t.Errorf("stop classification/count=%v/%d, failed=%t", err, tr.stops.Load(), stopFails)
					}
				})
			})
		}
	}
}
func TestHeaderRejectionDaemon(t *testing.T) {

	for _, status := range []int{401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tr := &headerFailure{status: status, stopFails: true}
				c, err := newExpiredAllocationFixtureClient(t, cloudhttp.Config{Transport: tr})
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
				<-time.After(2 * time.Minute)
				synctest.Wait()
				wantStops := int32(1)
				if status == 401 {
					wantStops = 0
				}
				if tr.online.Load() != 1 || tr.stops.Load() != wantStops {
					t.Errorf("HTTP %d + body timeout + failed cleanup: online-link/stop=%d/%d, want 1/%d (operator-blocked)", status, tr.online.Load(), tr.stops.Load(), wantStops)
				}
				t.Logf("HTTP %d acquisition/stop attempts after two virtual minutes=%d/%d", status, tr.online.Load(), tr.stops.Load())
			})
		})
	}
}

// A long outage that fails requests immediately protects against a retry loop
// masked by thirty-second request timeouts in the original outage probe.
type fastOutage struct{ cloud *independentOutageCloud }

func (tr fastOutage) RoundTrip(r *http.Request) (*http.Response, error) {
	if tr.cloud.offline.Load() && r.URL.Path != "/v1/device-p2p/stop" {
		tr.cloud.mu.Lock()
		tr.cloud.calls = append(tr.cloud.calls, time.Now())
		tr.cloud.mu.Unlock()
		return nil, syscall.ECONNREFUSED
	}
	return tr.cloud.RoundTrip(r)
}
func TestEightHourFastOutage(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		oracle := &independentOutageCloud{}
		client, err := newFixtureClient(t, cloudhttp.Config{Transport: fastOutage{oracle}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s, err := start(ctx, Config{}, func(s *Session) sessionedge.Factory {
			return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
				trace := []string{}
				p := newSimulatedWire(t, &trace)
				p.outage = oracle.offline.Load
				return reviewWire{p, oracle, t}
			}}
		})
		if err != nil {
			client.Close()
			t.Fatal(err)
		}
		defer s.Close()
		first, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer first.Close()
		if err := exchange(first.(*stream), 81, 0); err != nil {
			t.Fatal(err)
		}
		oracle.offline.Store(true)
		<-time.After(8 * time.Hour)
		synctest.Wait()
		select {
		case <-s.Done():
			t.Fatalf("daemon exited during fail-fast outage: %v", s.Close())
		default:
		}
		oracle.mu.Lock()
		requests := len(oracle.calls)
		if requests < 900 || requests > 2000 {
			t.Errorf("8h immediate-failure request count=%d, want 900..2000", requests)
		}
		if oracle.allocs != 1 || oracle.stops["review-1"] != 1 || len(oracle.stopDurations) != 1 || oracle.stopDurations[0] != 5*time.Second {
			t.Errorf("outage allocs/stops/durations=%d/%v/%v", oracle.allocs, oracle.stops, oracle.stopDurations)
		}
		oracle.mu.Unlock()
		if oracle.live.Load() != 0 {
			t.Error("old wire remains live")
		}
		oracle.offline.Store(false)
		<-time.After(70 * time.Second)
		synctest.Wait()
		next, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer next.Close()
		if err := exchange(next.(*stream), 82, 1024); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		oracle.mu.Lock()
		defer oracle.mu.Unlock()
		if oracle.allocs != 2 || oracle.stops["review-2"] != 1 || len(oracle.violations) > 0 {
			t.Errorf("recovery allocs/stops/violations=%d/%v/%v", oracle.allocs, oracle.stops, oracle.violations)
		}
		t.Logf("8h immediate failures: requests=%d allocations=%d stops=%v stop duration=%v", requests, oracle.allocs, oracle.stops, oracle.stopDurations)
	})
}
