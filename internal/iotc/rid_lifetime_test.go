package iotc

import (
	"bytes"
	"fmt"
	"github.com/go-json-experiment/json"
	"os"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func TestRIDClosedHistoryDoesNotRefuseCurrentAttempt(t *testing.T) {
	for _, elapsed := range []time.Duration{0, 19999 * time.Millisecond, 20 * time.Second} {
		t.Run(fmt.Sprint(elapsed), func(t *testing.T) {
			old := newMatrixPeer(t, 1, "same-fields", UDP)
			old.prepare(0x0405)
			old.deliver(old.peerLink(), matrixKnock(t, 0x0405, "old"))
			old.progressed(0x0405)
			stalePunch := old.record(0x030d, nil)
			key := old.h.m.key
			out := old.step(Event{Kind: Cancel}) // Real local close, not just cancellation during setup.
			closes := 0
			for _, a := range out {
				if a.Kind == Send {
					q, err := DecodeLegacy(a.Data, key)
					if err == nil && q.Opcode == 0x0417 {
						closes++
					}
				}
			}
			if old.h.m.State() != Closed || closes != 3 {
				t.Fatalf("close state=%v packets=%d, want Closed and 3", old.h.m.State(), closes)
			}
			old.ignore(old.peerLink(), matrixKnock(t, 0x0405, "old"))

			// Deliberately inject the closed full RID despite our generation policy.
			// Marker 0 cannot veto otherwise current traffic.
			p := newMatrixPeer(t, 2, "same-fields", UDP)
			p.now = core.Instant(elapsed)
			p.rendezvous()
			if p.h.mat.RID != old.h.mat.RID {
				t.Fatal("control must reuse the entire closed RID")
			}
			p.ignore(p.server, stalePunch) // Same RID still needs current authentication.
			foreign := p.plain(0x030d)
			foreign[127] ^= 1
			p.ignore(p.server, p.record(0x030d, foreign)) // Current crypto still needs current RID.
			p.deliver(p.server, p.record(0x030d, nil))
			p.progressed(0x030d)
			p.deliver(p.peerLink(), matrixKnock(t, 0x0405, "old"))
			p.progressed(0x0405)
		})
	}
}

func TestRIDLANCodeFourIsOutsideOutboundProfile(t *testing.T) {
	// This profile never issues LAN search. An unsolicited literal 0602/code-4
	// reply cannot refuse its current RID or create a receive quarantine.
	b, err := os.ReadFile("testdata/rid-refusal.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Plain, Wire string }
	if err := json.Unmarshal(b, &fixture, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	q, err := DecodeLegacy(unhex(fixture.Wire), ScrambleKey([16]byte{}))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := q.Marshal()
	if err != nil || !bytes.Equal(raw, unhex(fixture.Plain)) || q.Opcode != 0x0602 || len(q.Body) != 184 || le32(q.Body[180:]) != 4 {
		t.Fatal("invalid literal LAN precheck code-4 fixture")
	}
	p := newMatrixPeer(t, 1, "same-fields", UDP)
	if [8]byte(q.Body[172:180]) != p.h.mat.RID {
		t.Fatal("refusal fixture must name current RID")
	}
	p.ignore(p.peerLink(), unhex(fixture.Wire))
	p.prepare(0x0405)
	p.deliver(p.peerLink(), matrixKnock(t, 0x0405, "old"))
	p.progressed(0x0405)
}
