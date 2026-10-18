package p2ptunnel

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func TestTelemetryReceivedControlValues(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	d.m.EnableTelemetry()
	a, b := d.open(445), d.open(4700)
	// Fragmented and repeated literal controls count only for their addressed slot.
	for _, tc := range []struct {
		name                string
		wire                []byte
		pause, resume, wake uint64
		state               byte
	}{
		{"initial", nil, 0, 0, 0, 0},
		{"partial pause header", []byte{5, a.Channel}, 0, 0, 0, 0},
		{"complete pause header", []byte{0, 0}, 1, 0, 0, 2},
		{"repeated pause", []byte{5, a.Channel, 0, 0}, 2, 0, 0, 4},
		{"wake", []byte{7, a.Channel, 0, 0}, 2, 0, 1, 2},
		{"resume", []byte{6, a.Channel, 0, 0}, 2, 1, 1, 0},
		{"redundant resume", []byte{6, a.Channel, 0, 0}, 2, 2, 1, 0},
		{"same stream", []byte{5, a.Channel, 0, 0, 7, a.Channel, 0, 0, 6, a.Channel, 0, 0}, 3, 3, 2, 0},
		{"close then ignored controls", []byte{4, a.Channel, 1, 0, 1, 5, a.Channel, 0, 0, 6, a.Channel, 0, 0, 7, a.Channel, 0, 0}, 3, 3, 2, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wire != nil {
				d.step(Event{Kind: Stream, Data: tc.wire})
			}
			got := d.m.Telemetry(a.Channel, d.now)
			if got.PauseReceived != tc.pause || got.ResumeReceived != tc.resume || got.WakeReceived != tc.wake || d.m.Channel(a.Channel).ReadState != tc.state {
				t.Fatalf("received=(%d,%d,%d) state=%d want (%d,%d,%d) state=%d", got.PauseReceived, got.ResumeReceived, got.WakeReceived, d.m.Channel(a.Channel).ReadState, tc.pause, tc.resume, tc.wake, tc.state)
			}
			other := d.m.Telemetry(b.Channel, d.now)
			if other.PauseReceived != 0 || other.ResumeReceived != 0 || other.WakeReceived != 0 || d.m.Channel(b.Channel).ReadState != 0 {
				t.Fatalf("controls attributed to sibling: %+v", other)
			}
		})
	}
}

func TestTelemetryReclaimValuesAndStages(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ChannelBytes, cfg.SessionBytes, cfg.HighWater = 8192, 8192, 1024
	d := activeDriver(t, cfg, false)
	d.m.EnableTelemetry()
	a, b, rpc := d.open(445), d.open(445), d.open(4700)
	inject := func(id byte, header []byte, size int) []Action {
		t.Helper()
		wire := append(bytes.Clone(header), bytes.Repeat([]byte{0x9a}, size)...)
		wire[1] = id
		return d.step(Event{Kind: Stream, Data: wire})
	}
	first := mustAction(t, inject(a.Channel, []byte{2, 0, 0, 16}, 4096), WriteLocal)
	staleA := mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: 1024}, first)), WriteLocal)
	staleB := mustAction(t, inject(b.Channel, []byte{2, 0, 0, 16}, 4096), WriteLocal)
	if got := d.m.Usage().RXBytes; got != 7168 {
		t.Fatalf("before reclaim bytes=%d want 7168", got)
	}
	d.now = core.Instant(2 * time.Second)
	out := inject(rpc.Channel, []byte{2, 0, 0, 32}, 8192)
	var closed []byte
	for _, action := range out {
		if action.Kind == CloseLocal {
			closed = append(closed, action.Channel)
		}
	}
	if !reflect.DeepEqual(closed, []byte{b.Channel, a.Channel}) {
		t.Fatalf("victims=%v want B then A", closed)
	}
	write := mustAction(t, out, WriteLocal)
	if write.Channel != rpc.Channel || len(write.Data) != 8192 {
		t.Fatalf("recipient write=%+v", write)
	}
	type stages struct {
		admitted, delivered, discarded, reclaims uint64
		retained, delivery                       int
		active                                   bool
	}
	check := func(id byte, want stages) {
		t.Helper()
		s, c, q := d.m.Telemetry(id, d.now), d.m.Channel(id), d.m.QueueStats(id, d.now)
		got := stages{s.AdmittedBytes, s.DeliveredBytes, s.DiscardedBytes, s.Reclaims, c.Queued, q.DeliveryBytes, c.Active}
		if got != want || s.Rejects != 0 || s.AdmittedPackets != 1 {
			t.Fatalf("slot %d stages=%+v want %+v; telemetry=%+v", id, got, want, s)
		}
		if s.AdmittedBytes != s.DeliveredBytes+s.DiscardedBytes+uint64(c.Queued) {
			t.Fatalf("slot %d stage conservation=%+v queue=%d", id, s, c.Queued)
		}
	}
	check(a.Channel, stages{4096, 1024, 3072, 1, 0, 0, false})
	check(b.Channel, stages{4096, 0, 4096, 1, 0, 0, false})
	check(rpc.Channel, stages{8192, 0, 0, 0, 8192, 8192, true})
	// In-flight victim completions cannot consume the recipient's storage.
	d.step(local(Event{Kind: LocalWritten, Count: 3072}, staleA))
	d.step(local(Event{Kind: LocalWritten, Count: 4096}, staleB))
	check(a.Channel, stages{4096, 1024, 3072, 1, 0, 0, false})
	check(b.Channel, stages{4096, 0, 4096, 1, 0, 0, false})
	check(rpc.Channel, stages{8192, 0, 0, 0, 8192, 8192, true})
	if got := d.m.Usage().RXBytes; got != 8192 {
		t.Fatalf("after reclaim bytes=%d want 8192", got)
	}
	write = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: 123}, write)), WriteLocal)
	check(rpc.Channel, stages{8192, 123, 0, 0, 8069, 8069, true})
	d.step(local(Event{Kind: LocalWriteFailed}, write))
	check(rpc.Channel, stages{8192, 123, 8069, 0, 0, 0, true})
	if got := d.m.Usage().RXBytes; got != 0 {
		t.Fatalf("drained bytes=%d want 0", got)
	}
}

func TestTelemetryProgressAgeValues(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "off"
		if enabled {
			name = "on"
		}
		t.Run(name, func(t *testing.T) {
			d := activeDriver(t, DefaultConfig(), false)
			if enabled {
				d.m.EnableTelemetry()
			}
			a := d.open(4700)
			at := func(ms int) { d.now = core.Instant(time.Duration(ms) * time.Millisecond) }
			check := func(ms int, backlog, progress time.Duration, retained, delivery int) {
				t.Helper()
				got := d.m.QueueStats(a.Channel, core.Instant(time.Duration(ms)*time.Millisecond))
				if got.BacklogAge != backlog || got.ProgressAge != progress || got.RXBytes != retained || got.DeliveryBytes != delivery {
					t.Fatalf("at %dms queue=%+v want backlog=%v progress=%v retained=%d delivery=%d", ms, got, backlog, progress, retained, delivery)
				}
			}
			at(1000)
			first := mustAction(t, d.step(Event{Kind: Stream, Data: []byte{2, a.Channel, 5, 0, 1, 2, 3, 4, 5}}), WriteLocal)
			check(1250, 250*time.Millisecond, 250*time.Millisecond, 5, 5)
			at(1500)
			next := mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: 2}, first)), WriteLocal)
			check(1750, 750*time.Millisecond, 250*time.Millisecond, 3, 3)
			check(1000, 500*time.Millisecond, 0, 3, 3) // clamp to last accepted Step
			at(2000)
			d.step(local(Event{Kind: LocalWritten, Count: 3}, first)) // stale token
			check(2000, time.Second, 500*time.Millisecond, 3, 3)
			at(2250)
			d.step(local(Event{Kind: LocalWritten, Count: 0}, next))
			check(2500, 1500*time.Millisecond, time.Second, 3, 0)
			at(2750)
			d.step(Event{Kind: Stream, Data: []byte{2, a.Channel, 1, 0, 6}})
			check(3000, 2*time.Second, 1500*time.Millisecond, 4, 0) // arrivals are not progress
			at(3250)
			next = mustAction(t, d.step(local(Event{Kind: LocalWritable}, a)), WriteLocal)
			at(3500)
			next = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: 3}, next)), WriteLocal)
			at(3750)
			d.step(local(Event{Kind: LocalWritten, Count: 1}, next))
			check(4000, 0, 0, 0, 0)
			at(5000)
			d.step(Event{Kind: Stream, Data: []byte{2, a.Channel, 2, 0, 7, 8}})
			check(5250, 250*time.Millisecond, 250*time.Millisecond, 2, 2)
			at(5500)
			d.step(Event{Kind: Stream, Data: []byte{4, a.Channel, 1, 0, 1}})
			check(6000, 0, 0, 0, 0)
			if !enabled && d.m.telemetry != nil {
				t.Fatal("off-mode allocated telemetry storage")
			}
		})
	}
}
