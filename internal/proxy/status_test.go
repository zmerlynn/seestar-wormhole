package proxy

import (
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"testing"
)

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
