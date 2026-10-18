package iotc

import (
	"testing"
	"time"
)

func TestDirectLinkFailure(t *testing.T) {
	h := newHarness(t, "direct", 0)
	h.step(Event{Kind: Start})
	h.run(time.Second)
	if h.m.state != Established {
		t.Fatal("setup did not establish")
	}
	h.step(Event{Kind: LinkFailed, Link: h.m.path})
	if h.m.state != Closed {
		t.Fatalf("selected direct link failed: got state %v, want Closed (%v)", h.m.state, Closed)
	}
}

func TestSelectedRelayLinkFailure(t *testing.T) {
	for _, mode := range []string{"relay", "tcp"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, mode, 0)
			h.step(Event{Kind: Start})
			h.run(6 * time.Second)
			if h.m.state != Established {
				t.Fatal("setup did not establish")
			}
			l := h.m.path
			h.step(Event{Kind: LinkFailed, Link: l})
			if h.m.state != Closed || h.disposed != 1 {
				t.Fatalf("failure: state=%v disposed=%d", h.m.state, h.disposed)
			}
			h.step(Event{Kind: LinkFailed, Link: l})
			if h.disposed != 1 {
				t.Fatal("repeated failure disposed twice")
			}
		})
	}
}
