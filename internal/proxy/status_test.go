package proxy

import (
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
		{session.Busy, diagnostic.Wrap("cloud", "unauthorized", session.ErrTransport), false, "Sign-in required — correct credentials and Start again"},
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
