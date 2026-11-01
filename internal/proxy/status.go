package proxy

import (
	"errors"
	"sync/atomic"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Status is a nonblocking latest-value projection. Only fixed UI text is
// published alongside an escaped public model label and full serial; it never retains
// events, dependency errors, or private cloud material.
// One session owner writes; any number of readers may load.
type Status struct {
	text      atomic.Pointer[string]
	selected  atomic.Pointer[string]
	ready     atomic.Bool
	armed     atomic.Bool
	updates   chan string
	rejected  chan struct{}
	reason    string
	everReady bool
}

// NewStatus enables a nonblocking terminal-rejection notification for the UI.
func NewStatus() *Status {
	return &Status{rejected: make(chan struct{}, 1), updates: make(chan string, 64)}
}
func (s *Status) Rejected() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.rejected
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
	if (e.Kind == session.Failed || e.Kind == session.Released) && e.Attempt != before.Attempt && !(e.Kind == session.Released && e.Attempt == 0 && before == after) {
		return
	}
	phase, reason := diagnostic.Fields(e.Err, "")
	if e.Kind == session.Failed || e.Kind == session.Released {
		permanent := false
		diagnostic.EachTerminal(e.Err, func(t *diagnostic.Terminal) { permanent = permanent || t.Permanent })
		if permanent || reason == "unauthorized" || reason == "reauthentication_required" {
			select {
			case s.rejected <- struct{}{}:
			default:
			}
		}
	}
	if e.Err != nil {
		switch {
		case reason == "unauthorized" || reason == "reauthentication_required":
			s.reason = "signin"
		case errors.Is(e.Err, session.ErrStop), errors.Is(e.Err, session.ErrUncertain), errors.Is(e.Err, session.ErrReleaseAbandoned):
			s.reason = "cleanup"
		case errors.Is(e.Err, session.ErrBusy):
			s.reason = "busy"
		case (phase == "cloud" && reason == "device_offline") ||
			(phase == "rendezvous" && (reason == "device_offline" || reason == "connection_timeout")):
			// Retain positive evidence through cleanup, backoff and the next
			// dial. A fresh run has no evidence and starts with Connecting.
			s.reason = "unreachable"
		default:
			s.reason = "transport"
		}
	}
	if before.State == session.Ready && after.State == session.Releasing && e.Err == nil {
		s.reason = "idle"
	}
	if after.State == session.Ready {
		s.ready.Store(true)
		s.everReady = true
		s.reason = ""
	}
	if after.State == session.Dormant && (s.reason == "busy" || s.reason == "idle" || s.reason == "transport" || s.reason == "unreachable") {
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
	case after.State == session.Dormant:
		text = "Waiting for a local client"
	case s.reason == "busy":
		text = "Busy — waiting for the other session"
	case after.State == session.Releasing && s.reason == "idle":
		text = "Releasing idle session"
	case after.State == session.Closed:
		text = "Stopped"
	case s.reason == "unreachable":
		text = "Seestar not reachable"
	case after.State == session.Backoff || s.everReady:
		text = "Reconnecting"
	}
	s.text.Store(&text)
	s.notify(text)
}

// MarkArmed is called only after authentication/catalog selection, listeners,
// and optional discovery have initialized. Ready alone is not local readiness.
func (s *Status) MarkArmed() {
	if s != nil {
		s.armed.Store(true)
		s.notify(s.Text())
	}
}
func (s *Status) SaveBoundary(lazy bool) bool {
	return s != nil && s.armed.Load() && (lazy || s.ready.Load())
}
func (s *Status) WasReady() bool { return s != nil && s.ready.Load() }
func (s *Status) Updates() <-chan string {
	if s == nil {
		return nil
	}
	return s.updates
}
func (s *Status) notify(text string) {
	select {
	case s.updates <- text:
	default:
	}
}

// SetSelectedDevice publishes only the same public identity as device_selected.
func (s *Status) SetSelectedDevice(d cloudhttp.Device) {
	if s != nil {
		label := d.DisplayName()
		s.selected.Store(&label)
	}
}
func (s *Status) SelectedDevice() string {
	if s != nil {
		if label := s.selected.Load(); label != nil {
			return *label
		}
	}
	return ""
}
