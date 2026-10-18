package iotc

import (
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"net/netip"
	"testing"
	"time"
)

func TestLateKnockCannotBeatDeadline(t *testing.T) {
	m := established(t, true)
	m.state = Connecting
	q := knock(0x0405, m.cfg.UID, m.material.RID)
	q.Body[28] = 1
	b, _ := EncodeLegacy(q, m.key)
	out := m.Step(Event{Kind: Wire, Attempt: 1, Link: m.path, Data: b}, core.Instant(61*time.Second))
	for _, a := range out {
		if a.Kind == Ready {
			t.Fatal("emitted Ready after 60-second attempt deadline before delayed Tick")
		}
	}
}

func TestAttemptDeadlineAtIngress(t *testing.T) {
	for _, kind := range []EventKind{Wire, Decrypted, Derived, Connected} {
		for _, at := range []time.Duration{60*time.Second - time.Nanosecond, 60 * time.Second, 60*time.Second + time.Nanosecond} {
			t.Run(fmt.Sprintf("%d/%s", kind, at), func(t *testing.T) {
				m := established(t, true)
				m.state = Connecting
				l := Link{Transport: TCP, Address: netip.MustParseAddrPort("203.0.113.1:443"), ID: 10}
				idx, _ := m.cache.Install(Server{Address: l.Address}, Marker{10, 1})
				m.add(endpoint{link: l, cache: idx})
				e := Event{Kind: kind, Attempt: 1, Link: l, Token: 99}
				switch kind {
				case Wire:
					q := knock(0x0405, m.cfg.UID, m.material.RID)
					q.Body[28] = 1
					e.Data, _ = EncodeLegacy(q, m.key)
					e.Link = m.path
				case Connected:
					m.endpoints[0].master = true
				case Derived:
					m.endpoints[0].connected = true
					m.cache.Entries[idx].Pending = 99
					m.cache.Entries[idx].RequestedMarker = Marker{10, 1}
					e.Key = 3
					e.IV = [12]byte{8}
				case Decrypted:
					m.endpoints[0].stage = precheckPending
					q := controlPacket(0x0218, 0x24, 56)
					put16(q.Body, m.material.P2PNonce)
					put32(q.Body[4:], 0x55)
					copy(q.Body[8:], m.cfg.UID[:])
					e.Data, _ = q.Marshal()
					m.pending[0] = operation{token: 99, kind: Open, link: l, typ: 3, size: len(e.Data)}
				}
				out := m.Step(e, core.Instant(at))
				if at < 60*time.Second {
					if m.state == Closed {
						t.Fatal("closed before deadline")
					}
					advanced := m.state == Established || m.endpoints[0].stage == precheckReady
					for _, a := range out {
						advanced = advanced || a.Kind == Seal
					}
					if !advanced {
						t.Fatal("valid completion did not advance before deadline")
					}
				} else {
					if m.state != Closed {
						t.Fatalf("overdue completion left state %v", m.state)
					}
					for _, a := range out {
						if a.Kind == Ready || a.Kind == Seal || a.Kind == OpenTCP || a.Kind == Derive {
							t.Fatalf("advanced after deadline: %v", a.Kind)
						}
					}
				}
				m.Step(Event{Kind: Tick, Attempt: 1}, core.Instant(61*time.Second))
				if at >= 60*time.Second && m.state != Closed {
					t.Fatal("late Tick revived attempt")
				}
			})
		}
	}
}
