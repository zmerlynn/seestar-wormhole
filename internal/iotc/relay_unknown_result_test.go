package iotc

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

// Unknown results do not refuse or advance; UDP task retries at 500 ms and
// retains the original 60000-ms lifetime.
func TestUnknownRelayResultRetries(t *testing.T) {
	for _, result := range []string{"9dffffff", "01000000", "ffffff7f"} {
		m := terminalMachine(t, 1)
		m.selected = UDP
		m.endpoints[0].link.Transport = UDP
		m.endpoints[0].peerNAT = 10
		idx, err := m.cache.Install(Server{Address: m.endpoints[0].link.Address}, Marker{10, 1})
		if err != nil {
			t.Fatal(err)
		}
		m.endpoints[0].cache = idx
		m.cache.Entries[idx].Key = 3
		m.cache.Entries[idx].CachedMarker = Marker{10, 1}
		raw := unhex("04021d00200000000d022400000000004558414d504c4555494430303030303030303030" + result + "4433221188776655")
		m.pending[0] = operation{token: 100, kind: Open, typ: 4, sid: 0x55, link: m.endpoints[0].link, size: len(raw)}
		out := m.Step(Event{Kind: Decrypted, Attempt: 1, Token: 100, Data: raw}, 0)
		if m.state != Connecting || m.endpoints[0].stage != rendezvousPending || m.progress != (RelayProgress{}) || m.endpoints[0].failure != 0 {
			t.Fatalf("unknown result changed relay state: %v %+v %+v", m.state, m.endpoints[0], m.progress)
		}
		if effects := nonTimerEffects(out); len(effects) != 0 {
			t.Errorf("unexpected non-timer effect: %+v", out)
		}
		for _, at := range []time.Duration{499 * time.Millisecond, 500 * time.Millisecond, 59999 * time.Millisecond} {
			out = m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(at))
			seals := 0
			for _, a := range out {
				if a.Kind == Seal {
					seals++
				}
			}
			if at == 499*time.Millisecond && seals != 0 || at == 500*time.Millisecond && seals != 1 {
				t.Errorf("retry at %v: seals=%d", at, seals)
			}
			if m.state != Connecting {
				t.Fatalf("task expired at %v", at)
			}
		}
		m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(60*time.Second))
		if m.state != Closed {
			t.Fatal("unknown result extended task lifetime")
		}
	}
}
