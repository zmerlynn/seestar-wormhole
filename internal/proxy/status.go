package proxy

import (
	"errors"
	"sync/atomic"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Status is a nonblocking latest-value projection. Only fixed UI text is
// published; it never retains events, dependency errors, or cloud material.
// One session owner writes; any number of readers may load.
type Status struct {
	text      atomic.Pointer[string]
	reason    string
	everReady bool
}

func (s *Status) Text() string {
	if s == nil {
		return "Connecting"
	}
	if v := s.text.Load(); v != nil {
		return *v
	}
	return "Connecting"
}
func (s *Status) observe(e session.Event, before, after session.Snapshot) {
	if s == nil {
		return
	}
	if e.Err != nil {
		switch {
		case errors.Is(e.Err, session.ErrStop), errors.Is(e.Err, session.ErrUncertain), errors.Is(e.Err, session.ErrReleaseAbandoned):
			s.reason = "cleanup"
		case errors.Is(e.Err, session.ErrBusy):
			s.reason = "busy"
		default:
			_, reason := diagnostic.Fields(e.Err, "")
			if reason == "unauthorized" || reason == "reauthentication_required" {
				s.reason = "signin"
			} else {
				s.reason = "transport"
			}
		}
	}
	if before.State == session.Ready && after.State == session.Releasing && e.Err == nil {
		s.reason = "idle"
	}
	if after.State == session.Ready {
		s.everReady = true
		s.reason = ""
	}
	text := "Connecting"
	switch {
	case after.State == session.Ready:
		text = "Ready"
	case s.reason == "signin":
		text = "Sign-in required — correct credentials and Start again"
	case after.OwnershipRecovery || s.reason == "cleanup":
		text = "Reconnecting — waiting for cleanup/retry"
	case s.reason == "busy":
		text = "Busy — waiting for the other session"
	case after.State == session.Releasing && s.reason == "idle":
		text = "Releasing idle session"
	case after.State == session.Dormant:
		text = "Waiting for a local client"
	case after.State == session.Closed:
		text = "Stopped"
	case after.State == session.Backoff || s.everReady:
		text = "Reconnecting"
	}
	s.text.Store(&text)
}
