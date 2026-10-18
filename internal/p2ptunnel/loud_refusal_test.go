package p2ptunnel

import (
	"strings"
	"testing"
)

func TestTunnelLossRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, rule string
		trigger    func(*Machine, uint64)
	}{
		{"replace", "R065", func(m *Machine, _ uint64) {
			b, _ := (Setup{Port: 80, Address: [4]byte{127, 0, 0, 1}}).Encode()
			m.onFrame(Frame{Command: SetupCommand, Channel: 7, Payload: b})
		}},
		{"write_failed_upload_open", "R069", func(m *Machine, g uint64) {
			m.Step(Event{Kind: LocalWriteFailed, Channel: 7, Generation: g, Token: 3}, 0)
		}},
		{"write_failed_upload_ended", "R069", func(m *Machine, g uint64) {
			m.channels[7].readEnd = true
			m.Step(Event{Kind: LocalWriteFailed, Channel: 7, Generation: g, Token: 3}, 0)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Role = Server
			cfg.AllowedPorts = []uint16{80}
			m, _ := New(cfg)
			m.status = Active
			m.activate(7, 42, false)
			c := &m.channels[7]
			g := c.generation
			c.queue.push([]byte("abcdef"))
			m.rxBytes = 6
			c.writeToken = 3
			c.writeCount = 6
			// An obsolete completion cannot close the live channel.
			m.Step(Event{Kind: LocalWriteFailed, Channel: 7, Generation: g, Token: 2}, 0)
			if !c.active || m.rxBytes != 6 {
				t.Fatal("stale completion changed channel")
			}
			m.actions = nil
			tc.trigger(m, g)
			var closed *Action
			for _, a := range m.actions {
				if a.Kind == CloseLocal {
					v := a
					closed = &v
				}
			}
			if closed == nil || closed.Rule != tc.rule || closed.Channel != 7 || closed.Generation != g || closed.DiscardedBytes != 6 || closed.Err == nil || !strings.Contains(closed.Err.Error(), "discarded 6 bytes") {
				t.Fatalf("close = %+v", closed)
			}
			if m.rxBytes != 0 {
				t.Fatalf("retained %d bytes", m.rxBytes)
			}
			if tc.rule == "R069" {
				if c.active {
					t.Fatal("failed channel still active")
				}
				found := false
				for _, a := range m.actions {
					if a.Kind == Send && a.Command == CloseCommand {
						f, e := Decode(a.Data, Established)
						if e == nil && f.Channel == 7 && len(f.Payload) == 1 && f.Payload[0] == 1 {
							found = true
						}
					}
				}
				if !found {
					t.Fatal("missing correlated CLOSE result 1")
				}
			} else if !c.active || c.generation == g || !c.connecting {
				t.Fatal("replacement semantics changed")
			}
		})
	}
}
