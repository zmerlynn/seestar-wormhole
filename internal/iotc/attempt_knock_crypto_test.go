package iotc

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func TestAttemptKnockBeforePunchCompletion(t *testing.T) {
	for _, op := range []uint16{0x0401, 0x0402, 0x0404, 0x0405, 0x0406} {
		for _, identity := range []string{"same-short-ids", "different-fields"} {
			for _, address := range []string{"198.51.100.20:41000", "192.0.2.20:41000"} {
				for _, retry := range []bool{false, true} {
					t.Run(fmt.Sprintf("%04x/%s/%s/retry=%v", op, identity, address, retry), func(t *testing.T) {
						l := Link{Address: netip.MustParseAddrPort(address)}
						old := newMatrixPeer(t, 1, identity, UDP)
						old.prepare(op)
						if op == 0x0404 || op == 0x0406 {
							old.deliver(l, matrixKnock(t, 0x0401, "old"))
						}
						saved := matrixKnock(t, op, "old")
						old.deliver(l, saved)
						old.progressed(op)
						old.step(Event{Kind: Cancel})
						p := newMatrixPeer(t, 2, identity, UDP)
						p.rendezvous()
						p.ignore(l, saved) // Before the punch even starts authentication.
						var held Action
						for _, a := range p.step(Event{Kind: Wire, Link: p.server, Data: p.record(0x030d, nil)}) {
							if a.Kind == Open {
								held = a
							} else if a.Kind != ArmTimer {
								t.Fatalf("punch emitted %v", a.Kind)
							}
						}
						if held.Token == 0 || p.h.m.candidateCount != 0 {
							t.Fatal("punch was not held before authorization")
						}
						if retry {
							p.now += core.Instant(500 * time.Millisecond)
							p.run(p.step(Event{Kind: Tick}))
						}
						p.ignore(l, saved) // While the current punch's authentication is pending.
						p.run(p.step(p.open(held)))
						p.progressed(0x030d)
						if op == 0x0404 || op == 0x0406 {
							p.deliver(l, matrixKnock(t, 0x0401, p.knockIdentity()))
						}
						p.ignore(l, saved) // After crypto, with the current candidate authorized.
						p.deliver(l, matrixKnock(t, op, p.knockIdentity()))
						p.progressed(op)
						p.ignore(l, saved)
					})
				}
			}
		}
	}
}
