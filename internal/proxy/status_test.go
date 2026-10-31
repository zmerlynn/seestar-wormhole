package proxy

import (
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Delay the existing synthetic cloud's offline answer so each retry has a
// visible dialing interval. No HTTP client or socket is used by this peer.
type offlineStatusCloud struct {
	r3Cloud
	checks int
}

func (c *offlineStatusCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/v1/my-device/detail" {
		c.mu.Lock()
		c.checks++
		c.mu.Unlock()
		time.Sleep(2 * time.Second)
		return r3Response(200, `{"code":300406,"data":null}`, false), nil
	}
	return c.r3Cloud.RoundTrip(r)
}

func TestStatusOfflineRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		peer := &offlineStatusCloud{}
		client, err := newFixtureClient(t, cloudhttp.Config{Transport: peer})
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		device, err := client.SelectDevice(t.Context(), cloudhttp.Device{})
		if err != nil {
			t.Fatal(err)
		}
		status := NewStatus()
		s, err := startWithRNG(t.Context(), Config{Status: status}, func(s *Session) sessionedge.Factory {
			return &factory{s: s, cloud: client, device: device}
		}, zeroRetryRNG{})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		synctest.Wait()
		if got := status.Text(); got != "Connecting" {
			t.Fatalf("before evidence: %q", got)
		}
		begin := time.Now()
		for _, elapsed := range []time.Duration{2 * time.Second, 3 * time.Second, time.Minute, 5 * time.Minute} {
			time.Sleep(elapsed - time.Since(begin))
			synctest.Wait()
			got := status.Text()
			t.Logf("at %v: %q", elapsed, got)
			if got != "Seestar not reachable" {
				t.Errorf("offline evidence lost: %q", got)
			}
		}
		peer.mu.Lock()
		checks := peer.checks
		peer.mu.Unlock()
		if checks < 3 {
			t.Errorf("automatic retries stopped: checks=%d", checks)
		}
		select {
		case <-status.Rejected():
			t.Error("offline evidence rejected the UI run")
		default:
		}
	})
}

func TestStatusSilentDeviceRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		status := NewStatus()
		var offline atomic.Bool
		offline.Store(true)
		s, stacks, _ := workloadSession(t, t.Context(), func(p *simulatedWire) {
			p.outage = offline.Load
		}, Config{Status: status})
		defer s.Close()
		<-stacks
		synctest.Wait()
		begin := time.Now()
		for _, elapsed := range []time.Duration{59 * time.Second, 60 * time.Second, 61 * time.Second, 5 * time.Minute} {
			time.Sleep(elapsed - time.Since(begin))
			synctest.Wait()
			want := "Seestar not reachable"
			if elapsed < time.Minute {
				want = "Connecting"
			}
			got := status.Text()
			t.Logf("at %v: %q", elapsed, got)
			if got != want {
				t.Errorf("silent device status=%q, want %q", got, want)
			}
		}
		if len(stacks) < 1 {
			t.Error("silent device was not retried")
		}
		offline.Store(false)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		conn, err := dialAfterRecovery(ctx, s)
		if err != nil {
			t.Fatal("device did not recover", err)
		}
		defer conn.Close()
		if got := status.Text(); got != "Ready" {
			t.Errorf("recovery retained unreachable hint: %q", got)
		}
	})
}

func TestStatusUnreachableTransitions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		state session.State
		want  string
	}{
		{"retry", nil, session.Dialing, "Seestar not reachable"},
		{"ready", nil, session.Ready, "Ready"},
		{"no demand", nil, session.Dormant, "Waiting for a local client"},
		{"stop", nil, session.Closed, "Stopped"},
		{"busy", session.ErrBusy, session.Backoff, "Busy — waiting for the other session"},
		{"cleanup", session.ErrStop, session.Backoff, "Reconnecting — waiting for cleanup/retry"},
		{"sign-in", diagnostic.NewTerminal(session.Cloud, "unauthorized", session.ErrUncertain), session.Backoff, "Sign-in required — correct credentials and Start again"},
		{"other failure", session.ErrTransport, session.Backoff, "Reconnecting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStatus()
			snap := session.Snapshot{State: session.Backoff, Attempt: 2}
			s.observe(session.Event{Kind: session.Failed, Attempt: 2, Err: cloudTerminal(cloud.ErrOffline)}, snap, snap)
			if got := s.Text(); got != "Seestar not reachable" {
				t.Fatal(got)
			}
			// A stale failure cannot replace the current evidence.
			s.observe(session.Event{Kind: session.Failed, Attempt: 1, Err: session.ErrBusy}, snap, snap)
			s.observe(session.Event{Err: tc.err}, snap, session.Snapshot{State: tc.state, Attempt: 2})
			if got := s.Text(); got != tc.want {
				t.Fatalf("status=%q, want %q", got, tc.want)
			}
			if tc.state == session.Ready || tc.state == session.Dormant {
				s.observe(session.Event{}, session.Snapshot{}, session.Snapshot{State: session.Dialing})
				if got := s.Text(); got == "Seestar not reachable" {
					t.Fatal("stale unreachable evidence on later dial")
				}
			}
		})
	}
}

func TestStatusProjection(t *testing.T) {
	for _, tc := range []struct {
		state    session.State
		err      error
		recovery bool
		want     string
	}{
		{session.Dormant, nil, false, "Waiting for a local client"},
		{session.Dialing, nil, false, "Connecting"},
		{session.Ready, nil, false, "Ready"},
		{session.Backoff, session.ErrBusy, false, "Busy — waiting for the other session"},
		{session.Busy, diagnostic.NewTerminal(session.Cloud, "unauthorized", session.ErrUncertain), false, "Sign-in required — correct credentials and Start again"},
		{session.Backoff, session.ErrTransport, false, "Reconnecting"},
		{session.Dialing, nil, true, "Reconnecting — waiting for cleanup/retry"},
		{session.Closed, nil, false, "Stopped"},
	} {
		var s Status
		s.observe(session.Event{Err: tc.err}, session.Snapshot{}, session.Snapshot{State: tc.state, OwnershipRecovery: tc.recovery})
		if got := s.Text(); got != tc.want {
			t.Fatalf("state %v: %q, want %q", tc.state, got, tc.want)
		}
	}
	var s Status
	s.observe(session.Event{}, session.Snapshot{State: session.Ready}, session.Snapshot{State: session.Releasing})
	if s.Text() != "Releasing idle session" {
		t.Fatal(s.Text())
	}
	s.observe(session.Event{}, session.Snapshot{}, session.Snapshot{State: session.Ready})
	s.observe(session.Event{}, session.Snapshot{}, session.Snapshot{State: session.Dialing})
	if s.Text() != "Reconnecting" {
		t.Fatal(s.Text())
	}
}

func TestStatusRejectsPermanentFailuresWithoutBlocking(t *testing.T) {
	s := NewStatus()
	terminal := diagnostic.NewTerminal(session.Cloud, "unauthorized", session.ErrUncertain)
	terminal.Permanent = true
	for range 1000 {
		s.observe(session.Event{Kind: session.Failed, Err: terminal}, session.Snapshot{}, session.Snapshot{State: session.Busy})
	}
	select {
	case <-s.Rejected():
	default:
		t.Fatal("no terminal rejection notification")
	}
	if s.Text() != "Sign-in required — correct credentials and Start again" {
		t.Fatal(s.Text())
	}
	busy := NewStatus()
	busy.observe(session.Event{Kind: session.Failed, Err: session.ErrBusy}, session.Snapshot{}, session.Snapshot{State: session.Backoff})
	select {
	case <-busy.Rejected():
		t.Fatal("ordinary busy retry rejected")
	default:
	}
}

func TestStatusIgnoresStaleRejection(t *testing.T) {
	s := NewStatus()
	s.observe(session.Event{}, session.Snapshot{}, session.Snapshot{State: session.Ready, Attempt: 2})
	terminal := diagnostic.NewTerminal(session.Cloud, "unauthorized", session.ErrUncertain)
	terminal.Permanent = true
	s.observe(session.Event{Kind: session.Failed, Attempt: 1, Err: terminal}, session.Snapshot{State: session.Ready, Attempt: 2}, session.Snapshot{State: session.Ready, Attempt: 2})
	if s.Text() != "Ready" {
		t.Fatal(s.Text())
	}
	select {
	case <-s.Rejected():
		t.Fatal("stale failure stopped current run")
	default:
	}
}

func TestStatusActualAuthorizationOutcome(t *testing.T) {
	_, denied := cloud.ParseLogin(401, nil, false)
	s := NewStatus()
	s.observe(session.Event{Kind: session.Failed, Err: cloudTerminal(denied)}, session.Snapshot{}, session.Snapshot{State: session.Busy})
	if s.Text() != "Sign-in required — correct credentials and Start again" {
		t.Fatal(s.Text())
	}
	select {
	case <-s.Rejected():
	default:
		t.Fatal("authorization did not end UI run")
	}
}

func TestStatusAuthorizationDuringRelease(t *testing.T) {
	s := NewStatus()
	terminal := diagnostic.NewTerminal(session.Cloud, "reauthentication_required", session.ErrStop)
	s.observe(session.Event{Kind: session.Released, Attempt: 1, Err: terminal}, session.Snapshot{State: session.Releasing, Attempt: 1}, session.Snapshot{State: session.Backoff, Attempt: 1, OwnershipRecovery: true})
	if s.Text() != "Sign-in required — correct credentials and Start again" {
		t.Fatal(s.Text())
	}
	select {
	case <-s.Rejected():
	default:
		t.Fatal("release authorization failure did not end UI run")
	}
}

func TestStatusBusyReturnsToDormant(t *testing.T) {
	cfg := session.DefaultConfig()
	cfg.Idle = time.Minute
	cfg.LazyBusyWindow = session.LazyBusyWindow
	m, err := session.New(cfg, zeroRetryRNG{})
	if err != nil {
		t.Fatal(err)
	}
	s := NewStatus()
	step := func(e session.Event, now core.Instant) {
		before := m.Snapshot()
		m.Step(e, now)
		s.observe(e, before, m.Snapshot())
	}
	step(session.Event{Kind: session.Start}, 0)
	step(session.Event{Kind: session.Client, Client: 1}, 0)
	attempt := m.Snapshot().Attempt
	step(session.Event{Kind: session.Failed, Attempt: attempt, Layer: session.Cloud, Err: session.ErrBusy}, 1)
	step(session.Event{Kind: session.Released, Attempt: attempt}, 2)
	if s.Text() != "Busy — waiting for the other session" {
		t.Fatal(s.Text())
	}
	step(session.Event{Kind: session.Tick}, core.Instant(session.LazyBusyWindow))
	if got := m.Snapshot(); got.State != session.Dormant || got.Queued != 0 {
		t.Fatalf("not dormant after demand expired: %+v", got)
	}
	if s.Text() != "Waiting for a local client" {
		t.Error(s.Text())
	}
	step(session.Event{Kind: session.Client, Client: 2}, core.Instant(session.LazyBusyWindow)+1)
	if s.Text() != "Connecting" {
		t.Fatalf("stale busy reason on fresh demand: %s", s.Text())
	}
}

func TestSessionStatusWiring(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		peer := &r3Cloud{faultPath: "/v1/device-p2p/online-link", faultStatus: 401, faultBody: `{"code":401,"data":null}`}
		client, err := newFixtureClient(t, cloudhttp.Config{Transport: peer})
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		status := NewStatus()
		s, err := start(context.Background(), Config{Idle: time.Minute, Status: status}, func(s *Session) sessionedge.Factory {
			return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		synctest.Wait()
		if got := status.Text(); got != "Waiting for a local client" {
			t.Errorf("initial owner status=%q", got)
		}
		if conn, err := s.DialPort(t.Context(), 4700); err == nil {
			conn.Close()
			t.Fatal("authorization rejection admitted client")
		}
		synctest.Wait()
		// The allocation's 401 leaves ownership uncertain; after its cleanup
		// receipt the latest projection reports recovery, while rejection is latched.
		if got := status.Text(); got != "Reconnecting — waiting for cleanup/retry" {
			t.Errorf("latest owner status=%q", got)
		}
		select {
		case <-status.Rejected():
		default:
			t.Error("session did not notify terminal rejection")
		}
	})
}

func TestStatusSaveBoundaryRequiresArmedAndFirstReady(t *testing.T) {
	s := NewStatus()
	s.observe(session.Event{}, session.Snapshot{}, session.Snapshot{State: session.Ready})
	if s.SaveBoundary(false) {
		t.Fatal("Ready without local initialization saved")
	}
	s.MarkArmed()
	if !s.SaveBoundary(false) {
		t.Fatal("first Ready boundary missing")
	}
	s.observe(session.Event{}, session.Snapshot{State: session.Ready}, session.Snapshot{State: session.Backoff})
	if !s.SaveBoundary(false) {
		t.Fatal("first Ready receipt lost during retry")
	}
	lazy := NewStatus()
	lazy.MarkArmed()
	if !lazy.SaveBoundary(true) || lazy.SaveBoundary(false) {
		t.Fatal("lazy/eager boundaries conflated")
	}
}
