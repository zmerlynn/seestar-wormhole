package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"net/netip"
	"testing"
	"time"
)

func TestTCPSelectionGateBoundaries(t *testing.T) {
	for _, udpResponse := range []bool{false, true} {
		gate := 1500 * time.Millisecond
		if udpResponse {
			gate = 5 * time.Second
		}
		m, _ := New(config())
		m.started = true
		m.state = Connecting
		m.material = material()
		m.udpResponse = udpResponse
		l := Link{TCP, netip.MustParseAddrPort("203.0.113.1:443"), 10}
		idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
		m.cache.Entries[idx].Key = 3
		m.cache.Entries[idx].CachedMarker = Marker{10, 1}
		m.add(endpoint{link: l, cache: idx, stage: precheckReady, connected: true, sid: 0x55})
		m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(gate-time.Nanosecond))
		if m.selection {
			t.Fatalf("selected TCP before %s gate", gate)
		}
		m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(gate))
		if !m.selection || m.selected != TCP {
			t.Fatalf("did not select TCP at %s", gate)
		}
	}
}

func TestSimulatorConcurrentTransportSelection(t *testing.T) {
	cases := []struct {
		name                       string
		masterAt, precheckAt, gate time.Duration
		want                       Transport
	}{
		{"TCP at 1.5s", 2 * time.Second, 2 * time.Second, 1500 * time.Millisecond, TCP},
		{"UDP before 1.5s", 1400 * time.Millisecond, 1499 * time.Millisecond, 1500 * time.Millisecond, UDP},
		{"UDP before 5s", time.Millisecond, 4999 * time.Millisecond, 5 * time.Second, UDP},
		{"UDP exactly at 5s before Tick", time.Millisecond, 5 * time.Second, 5 * time.Second, UDP},
		{"TCP before late UDP", time.Millisecond, 5001 * time.Millisecond, 5 * time.Second, TCP},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "relay", 0)
			h.allowTCP = true
			h.impair = sim.Link{MTU: 1416, Delay: time.Millisecond}
			h.udpMasterAt = core.Instant(tc.masterAt)
			h.udpPrecheckAt = core.Instant(tc.precheckAt)
			h.step(Event{Kind: Start})
			h.run(tc.gate - 10*time.Millisecond)
			if h.m.selection {
				t.Fatal("transport selected before gate/UDP completion")
			}
			tcpReady := false
			for _, v := range h.m.endpoints[:h.m.count] {
				tcpReady = tcpReady || v.link.Transport == TCP && !v.master && v.stage == precheckReady
			}
			if !tcpReady {
				t.Fatal("TCP did not progress concurrently to precheck readiness")
			}
			h.run(30 * time.Millisecond)
			if h.ready != 1 || h.m.path.Transport != tc.want {
				t.Fatalf("ready=%d path=%v want=%v trace=%v", h.ready, h.m.path, tc.want, h.trace)
			}
			path := h.m.path
			h.run(time.Second)
			if h.ready != 1 || h.m.path != path {
				t.Fatal("late losing replies changed selected path")
			}
		})
	}
}
