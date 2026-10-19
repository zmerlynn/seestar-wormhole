package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

type outageCloud struct {
	simulatedCloud
	offline      atomic.Bool
	blockedStops atomic.Int32
}

func (c *outageCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	if c.offline.Load() {
		if r.URL.Path == "/v1/device-p2p/stop" {
			c.blockedStops.Add(1)
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	if r.URL.Path == "/v1/my-device" {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":{"items":[],"total":0}}`))}, nil
	}
	return c.simulatedCloud.RoundTrip(r)
}

func TestAllTrafficOutageReleaseRecovery(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cloud := &outageCloud{}
		var logs bytes.Buffer
		logger := diagnostic.Logger("json", &logs, -4)
		client, err := newFixtureClient(t, cloudhttp.Config{Transport: cloud, Logger: logger})
		if err != nil {
			t.Fatal(err)
		}
		stacks := make(chan *stack, 16)
		s, err := start(ctx, Config{Logger: logger}, func(s *Session) sessionedge.Factory {
			return &workloadFactory{&factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
				trace := []string{}
				p := newSimulatedWire(t, &trace)
				p.outage = cloud.offline.Load
				return p
			}}, stacks}
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		first := <-stacks
		if err := exchange(c.(*stream), 1, 0); err != nil {
			t.Fatal(err)
		}
		cloud.offline.Store(true)
		<-time.After(120 * time.Second)
		synctest.Wait()
		select {
		case <-s.Done():
			t.Fatalf("daemon exited during outage: %v", s.Close())
		default:
		}
		if cloud.blockedStops.Load() != 1 || cloud.stops.Load() != 0 {
			t.Fatalf("blocked/accepted stops=%d/%d, want 1/0", cloud.blockedStops.Load(), cloud.stops.Load())
		}
		assertDTLSWorkersExited(t, first)
		cloud.offline.Store(false)
		// An eager retry may still have an outstanding request that began offline.
		<-time.After(60 * time.Second)
		synctest.Wait()
		c2, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatalf("recovery: %v", err)
		}
		defer c2.Close()
		if err := exchange(c2.(*stream), 2, 7900); err != nil {
			t.Fatal(err)
		}
		if cloud.allocs.Load() != 2 {
			t.Fatalf("successful allocations=%d, want exactly 2", cloud.allocs.Load())
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		// Read the capture only after joining every log producer. A synctest
		// Wait does not make a bytes.Buffer safe while recovery can still log.
		if !strings.Contains(logs.String(), `"reason":"release_abandoned"`) {
			t.Fatalf("missing bounded abandonment diagnostic: %s", logs.String())
		}
		if cloud.stops.Load() != 1 {
			t.Fatalf("final accepted stops=%d, want 1", cloud.stops.Load())
		}
		t.Logf("120s all-traffic outage: abandoned stops=%d, allocations=%d, final accepted stops=%d", cloud.blockedStops.Load(), cloud.allocs.Load(), cloud.stops.Load())
	})
}

func TestAllocationNetworkFailureRetries(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		transport := &allocationOutage{}
		client, err := newFixtureClient(t, cloudhttp.Config{Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		a := &allocator{client, cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
		lease, err := a.Allocate(context.Background())
		if lease != nil || !errors.Is(err, session.ErrTransport) {
			t.Fatalf("network failure lease/error=%v/%v, want nil/retryable transport", lease, err)
		}
	})
}

type allocationOutage struct{ simulatedCloud }

func (c *allocationOutage) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/v1/device-p2p/online-link" {
		return nil, context.DeadlineExceeded
	}
	return c.simulatedCloud.RoundTrip(r)
}

func TestStartupSelectionNetworkRetry(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		tr := &catalogOutage{}
		c, err := newFixtureClient(t, cloudhttp.Config{Transport: tr})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		d, err := selectDevice(context.Background(), c, cloudhttp.Device{}, nil, zeroRetryRNG{})
		if err != nil || d.Serial != "sim" {
			t.Fatalf("startup selection=%v/%v, want recovery", d, err)
		}
		if tr.calls != 2 || tr.elapsed != 500*time.Millisecond {
			t.Fatalf("calls/delay=%d/%v, want 2/500ms", tr.calls, tr.elapsed)
		}
	})
}

type zeroRetryRNG struct{}

func (zeroRetryRNG) Uint64() uint64 { return 0 }

type catalogOutage struct {
	calls   int
	first   time.Time
	elapsed time.Duration
}

func (c *catalogOutage) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Query().Get("current") == "2" {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"items":[]}}`))}, nil
	}
	c.calls++
	if c.calls == 1 {
		c.first = time.Now()
		return nil, errors.New("unreachable fixture")
	}
	c.elapsed = time.Since(c.first)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200,"data":{"items":[{"deviceSn":"sim","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1}],"total":1}}`))}, nil
}

func TestCloudNetworkRecoveryPhaseMatrix(t *testing.T) {
	for _, path := range []string{"/v1/user/login", "/v1/user/current-profile", "/v1/my-device/detail", "/v1/device-p2p/online-link"} {
		t.Run(path, func(t *testing.T) {

			t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
			t.Setenv("SEESTAR_PASSWORD", "fixture-password")

			synctest.Test(t, func(t *testing.T) {
				tr := &phaseNetworkFailure{path: path}
				c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: tr})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				a := &allocator{c, cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
				lease, err := a.Allocate(context.Background())
				if lease != nil || !errors.Is(err, session.ErrTransport) {
					t.Fatalf("phase failure=%v/%v, want nil/retryable", lease, err)
				}
				lease, err = a.Allocate(context.Background())
				if lease == nil || err != nil {
					t.Fatalf("phase recovery=%v/%v, want lease/nil", lease, err)
				}
				if err := lease.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
				if tr.allocs.Load() != 1 || tr.stops.Load() != 1 {
					t.Fatalf("accepted allocation/stop=%d/%d, want 1/1", tr.allocs.Load(), tr.stops.Load())
				}
			})
		})
	}
}

type phaseNetworkFailure struct {
	simulatedCloud
	path   string
	failed bool
}

func (c *phaseNetworkFailure) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == c.path && !c.failed {
		c.failed = true
		return nil, errors.New("network unavailable")
	}
	return c.simulatedCloud.RoundTrip(r)
}

func TestStartupRetryCancellationAndConfiguration(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		tr := &outageCloud{}
		tr.offline.Store(true)
		c, err := newFixtureClient(t, cloudhttp.Config{Transport: tr, Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { _, err := selectDevice(ctx, c, cloudhttp.Device{}, nil, zeroRetryRNG{}); result <- err }()
		<-time.After(1250 * time.Millisecond) // first failed request, then the retry timer
		cancel()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("selection cancellation=%v", err)
		}
		tr.offline.Store(false)
		_, err = selectDevice(context.Background(), c, cloudhttp.Device{}, nil, zeroRetryRNG{})
		if !errors.Is(err, cloud.ErrConfig) {
			t.Fatalf("empty catalog=%v, want fatal configuration", err)
		}
	})
}

func TestPartialAllocationCleanupPreservesRefusal(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		tr := &refusedCleanupCloud{}
		c, err := newFixtureClient(t, cloudhttp.Config{Transport: tr})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		a := &allocator{c, cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
		lease, err := a.Allocate(context.Background())
		if lease != nil || !errors.Is(err, session.ErrBusy) || !errors.Is(err, session.ErrReleaseAbandoned) {
			t.Fatalf("refused allocation cleanup=%v/%v, want busy and abandonment", lease, err)
		}
		if tr.attempted != 1 {
			t.Fatalf("internal stop attempts=%d, want 1", tr.attempted)
		}
	})
}

type refusedCleanupCloud struct {
	simulatedCloud
	attempted int
}

func (c *refusedCleanupCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.URL.Path {
	case "/v1/device-p2p/online-link":
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":300404,"data":{"conId":"partial-allocation"}}`))}, nil
	case "/v1/device-p2p/stop":
		c.attempted++
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	return c.simulatedCloud.RoundTrip(r)
}
