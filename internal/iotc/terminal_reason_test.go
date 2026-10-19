package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"net/netip"
	"testing"
	"time"
)

func terminalMachine(t *testing.T, count int) *Machine {
	t.Helper()
	m, _ := New(config())
	m.started = true
	m.state = Connecting
	m.material = material()
	m.selection = true
	m.selected = TCP
	for i := 0; i < count; i++ {
		m.add(endpoint{link: Link{TCP, netip.AddrPortFrom(netip.AddrFrom4([4]byte{203, 0, 113, byte(i + 1)}), 443), uint64(i + 1)}, connected: true, stage: rendezvousPending, sid: 0x55})
	}
	return m
}
func deliverTerminal(m *Machine, i int, result int32, alert byte) []Action {
	var b []byte
	typ := byte(4)
	if alert != 0 {
		typ = 14
		b = []byte{0x55, 0, 0, 0, 0x34, 0x12, alert, 4, 29}
	} else {
		p := controlPacket(0x020d, 0x24, 32)
		copy(p.Body, m.cfg.UID[:])
		put32(p.Body[20:], uint32(result))
		copy(p.Body[24:], m.material.RID[:])
		b, _ = p.Marshal()
	}
	token := uint64(100 + i)
	m.pending[0] = operation{token: token, kind: Open, link: m.endpoints[i].link, typ: typ, sid: 0x55, size: len(b)}
	return m.Step(Event{Kind: Decrypted, Attempt: 1, Token: token, Data: b}, 0)
}
func TestTerminalRepliesAreActionable(t *testing.T) {
	cases := []struct {
		name   string
		result int32
		alert  byte
		reason string
	}{
		{"offline", 0, 1, "device offline"}, {"asleep", 0, 2, "device asleep"},
		{"device refusal", -1, 0, "device refused"}, {"server refusal", -2, 0, "server refused"},
		{"unknown UID", -3, 0, "device unknown"}, {"other request", -4, 0, "another relay request"},
		{"unsupported", -5, 0, "relay unsupported"}, {"multiple login", -6, 0, "multiple device logins"},
		{"sleep result", -8, 0, "device asleep"}, {"port", -9, 0, "port rejected"}, {"auth", -22, 0, "authentication rejected"},
		{"old auth", -23, 0, "authentication unsupported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := terminalMachine(t, 1)
			out := deliverTerminal(m, 0, tc.result, tc.alert)
			for _, a := range out {
				if a.Kind == Failed {
					if a.Failures == 0 || a.Failures.String() != tc.reason {
						t.Fatalf("typed failure=%v want=%q", a.Failures, tc.reason)
					}
					if a.Reason != tc.reason {
						t.Fatalf("reason=%q want=%q", a.Reason, tc.reason)
					}
					return
				}
			}
			t.Fatalf("terminal %s left state %v without actionable failure", tc.name, m.state)
		})
	}
}
func TestTerminalAlternativesRemainIndependent(t *testing.T) {
	m := terminalMachine(t, 2)
	deliverTerminal(m, 0, -22, 0)
	if m.state != Connecting {
		t.Fatal("one losing server killed competitor")
	}
	out := deliverTerminal(m, 1, -4, 0)
	found := false
	for _, a := range out {
		if a.Kind == Failed {
			found = true
			if a.Reason != "another relay request; authentication rejected" {
				t.Fatalf("aggregate reason=%q", a.Reason)
			}
		}
	}
	if !found {
		t.Fatal("all terminal alternatives became a timeout")
	}
	m = terminalMachine(t, 1)
	deliverTerminal(m, 0, -7, 0)
	if m.state != Connecting || m.endpoints[0].stage != rendezvousPending {
		t.Fatal("pending treated as terminal")
	}
	m = terminalMachine(t, 2)
	deliverTerminal(m, 0, -22, 0)
	out = m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(60*time.Second))
	for _, a := range out {
		if a.Kind == Failed && a.Reason == "connection timeout" {
			t.Fatal("timeout discarded known refusal")
		}
	}
}

func TestTerminalRelayDoesNotDiscardDirectCandidates(t *testing.T) {
	m := terminalMachine(t, 1)
	m.candidates[0] = netip.MustParseAddrPort("198.51.100.1:40000")
	m.candidateCount = 1
	deliverTerminal(m, 0, -22, 0)
	if m.state != Connecting {
		t.Fatal("relay refusal killed pending direct candidate")
	}
}
