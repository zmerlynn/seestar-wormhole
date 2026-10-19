package p2ptunnel

import (
	"bytes"
	"testing"
)

// Local scheduling bound, assuming complete RDT acceptance: with 256 channel
// slots a continuously pending control completes within 512 frames after an
// outstanding control (513 including it): one cyclic visit per slot and at most
// one FIFO frame between controls. The successor schedules assert the exact
// worst case; this schedule needs fewer frames.
// The kth queued DATA/lifecycle frame completes within 2*(k+1) frames, counting
// an already outstanding frame. Partial frames always finish without interleave.
// Zero acceptance cannot give a wall-clock bound; transport credit is required.
func TestBoundedServiceDelayAllSlots(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 100
	d := activeDriver(t, cfg, false)
	var ch [256]Action
	var writes [256]Action
	for i := range ch {
		ch[i] = d.open(445)
		writes[i] = mustAction(t, d.frame(Frame{DataCommand, ch[i].Channel, bytes.Repeat([]byte{'a'}, 101)}), WriteLocal)
	}
	d.auto = false
	send := mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: 101}, writes[0])), Send)
	for i := 1; i < len(ch); i++ {
		d.step(local(Event{Kind: LocalWritten, Count: 101}, writes[i]))
	}
	r := mustAction(t, d.step(local(Event{Kind: ReadReady}, ch[1])), ReadLocal)
	d.step(local(Event{Kind: LocalData, Data: []byte{'D'}}, r))
	r = mustAction(t, d.step(local(Event{Kind: ReadReady}, ch[2])), ReadLocal)
	d.step(local(Event{Kind: LocalEOF}, r))
	var resumed [256]bool
	dataAt, endAt := 0, 0
	var churn Action
	for completed := 1; completed <= 512; completed++ {
		if send.Data[0] == 6 {
			resumed[send.Data[1]] = true
		}
		if send.Data[0] == 2 {
			dataAt = completed
		}
		if send.Data[0] == 3 {
			endAt = completed
		}
		// Keep a lower ID eligible after each immutable handoff, indefinitely.
		if send.Data[1] == ch[0].Channel {
			if send.Data[0] == 6 {
				churn = mustAction(t, d.frame(Frame{DataCommand, ch[0].Channel, bytes.Repeat([]byte{'a'}, 101)}), WriteLocal)
			}
			if send.Data[0] == 5 {
				d.step(local(Event{Kind: LocalWritten, Count: 101}, churn))
			}
		}
		out := d.step(Event{Kind: Sent, Token: send.Token, Count: len(send.Data)})
		all := true
		for _, v := range resumed {
			all = all && v
		}
		if all && dataAt > 0 && endAt > 0 {
			t.Logf("all 256 RESUMEs serviced by completed frame %d; DATA=%d END=%d", completed, dataAt, endAt)
			if dataAt > 4 || endAt > 6 {
				t.Fatalf("FIFO service: DATA=%d END=%d; bounds 4/6", dataAt, endAt)
			}
			return
		}
		send = mustAction(t, out, Send)
	}
	t.Fatalf("service exceeded 512 completed frames; resumed=%v DATA=%d END=%d", resumed, dataAt, endAt)
}
