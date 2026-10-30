//go:build gui && guitest

package proxy

import "github.com/zmerlynn/seestar-wormhole/internal/session"

// GUITestObserve exercises the production status projection without an adapter.
// It is unavailable to plain gui and release builds.
func (s *Status) GUITestObserve(e session.Event, before, after session.Snapshot) {
	s.observe(e, before, after)
}
