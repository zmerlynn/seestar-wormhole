package p2ptunnel

import "testing"

func TestLiteralCloseOutcome(t *testing.T) {
	for _, localEnd := range []bool{false, true} {
		for _, wire := range [][]byte{{4, 0, 0, 0}, {4, 0, 1, 0, 0}, {4, 0, 1, 0, 1}, {4, 0, 1, 0, 2}, {4, 0, 1, 0, 255}} {
			d := activeDriver(t, DefaultConfig(), false)
			ch := d.open(4700)
			if localEnd {
				read := mustAction(t, d.step(local(Event{Kind: ReadReady}, ch)), ReadLocal)
				d.step(local(Event{Kind: LocalEOF}, read))
			}
			wire[1] = ch.Channel
			a := mustAction(t, d.step(Event{Kind: Stream, Data: wire}), CloseLocal)
			want := CloseOutcome{HasResult: len(wire) == 5, LocalEnd: localEnd}
			if want.HasResult {
				want.Result = wire[4]
			}
			if a.PeerClose == nil || *a.PeerClose != want {
				t.Fatalf("wire %x: outcome %+v want %+v", wire, a.PeerClose, want)
			}
			if d.m.Channel(ch.Channel).Active {
				t.Fatal("CLOSE did not release channel")
			}
		}
	}
}
