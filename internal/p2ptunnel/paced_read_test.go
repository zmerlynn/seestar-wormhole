package p2ptunnel

import (
	"bytes"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

// Replay observed application pressure without SMB/device I/O: sequential
// 256KiB replies, 16,594,560 bytes twice, a consumer paced at 204,800 B/s, and
// delayed reliable tunnel controls. Payload and delay are synthetic; the trace did
// not retain decrypted channel/control events, so this is a regression for a
// reproduced receive-budget defect, not proof of the device's closing cause.
func TestPacedRead(t *testing.T) {
	for _, latency := range []time.Duration{time.Millisecond, 100 * time.Millisecond} {
		t.Run(latency.String(), func(t *testing.T) {
			type delivery struct {
				side  int
				event Event
			}
			q := sim.New[delivery](2<<20, 4096)
			ac, sc := legacyCapacityConfig(), legacyCapacityConfig()
			sc.Role, sc.AllowedPorts = Server, []uint16{445}
			am, _ := New(ac)
			sm, _ := New(sc)
			machines := []*Machine{am, sm}
			timers := []*sim.Timer[delivery]{sim.NewTimer(q), sim.NewTimer(q)}
			schedule := func(at core.Instant, side int, e Event) {
				t.Helper()
				if err := q.Schedule(at, delivery{side, e}, len(e.Data)); err != nil {
					t.Fatal(err)
				}
			}
			const fileSize = 16594560
			const total = 2 * fileSize
			const batch = 256 << 10
			const rate = 204800
			var sockets [2]Action
			sent, received, available, peak, pauses := 0, 0, 0, 0, 0
			source, sink := sha256.New(), sha256.New()
			var sourceNext core.Instant
			var deliveryAt [2]core.Instant
			step := func(d delivery, now core.Instant) error {
				actions := machines[d.side].Step(d.event, now)
				assertBudget(t, machines[d.side])
				peak = max(peak, am.Usage().RXCapacity)
				for _, a := range actions {
					switch a.Kind {
					case Send:
						if d.side == 0 && a.Data[0] == byte(PauseCommand) {
							pauses++
						}
						delay := time.Millisecond
						if sent >= 14155776 {
							delay = latency
						}
						deliveryAt[d.side] = max(deliveryAt[d.side], now.Add(delay))
						schedule(deliveryAt[d.side], 1-d.side, Event{Kind: Stream, Data: a.Data})
						schedule(now, d.side, Event{Kind: Sent, Token: a.Token, Count: len(a.Data)})
						// A control arriving at the sender is an opportunity to resume reading.
						if d.side == 0 && a.Data[0] >= byte(PauseCommand) && a.Data[0] <= byte(WakeCommand) {
							schedule(deliveryAt[d.side], 1, local(Event{Kind: ReadReady}, sockets[1]))
						}
					case SessionReady:
						if d.side == 0 {
							schedule(now, 0, Event{Kind: Open, Port: 445, Request: 1})
						}
					case Opened:
						sockets[0] = a
					case ConnectLocal:
						sockets[1] = a
						available = min(batch, total)
						schedule(now, 1, local(Event{Kind: Connected}, a))
						schedule(now, 1, local(Event{Kind: ReadReady}, a))
					case ReadLocal:
						if d.side != 1 {
							t.Fatal("unexpected application read")
						}
						n := min(MaxData, available)
						data := make([]byte, n)
						for i := range data {
							data[i] = byte((sent+i)*31 + 7)
						}
						source.Write(data)
						sent += n
						available -= n
						sourceNext = max(sourceNext, now).Add(time.Millisecond)
						schedule(sourceNext, 1, local(Event{Kind: LocalData, Data: data}, a))
						if available > 0 {
							schedule(sourceNext, 1, local(Event{Kind: ReadReady}, a))
						}
					case WriteLocal:
						if d.side != 0 {
							t.Fatal("unexpected application write")
						}
						sink.Write(a.Data)
						received += len(a.Data)
						done := now.Add(time.Duration(len(a.Data)) * time.Second / rate)
						schedule(done, 0, local(Event{Kind: LocalWritten, Count: len(a.Data)}, a))
						if received < total && (received%fileSize%batch == 0 || received == fileSize) {
							// One outstanding reply at a time, including a short final file block.
							remaining := fileSize - received%fileSize
							available = min(batch, remaining, total-received)
							schedule(done.Add(latency), 1, local(Event{Kind: ReadReady}, sockets[1]))
						}
					case CloseLocal, SessionClosed:
						t.Fatalf("premature close after %d/%d bytes at %s: %s", received, total, time.Duration(now), a.Reason)
					case ArmTimer:
						if err := timers[d.side].Arm(a.At, delivery{d.side, Event{Kind: Tick}}); err != nil {
							t.Fatal(err)
						}
					case CancelTimer:
						timers[d.side].Cancel()
					}
				}
				return nil
			}
			schedule(0, 0, Event{Kind: Start})
			schedule(0, 1, Event{Kind: Start})
			if err := q.Run(core.Instant(5*time.Minute), 200000, step); err != nil {
				t.Fatal(err)
			}
			if sent != total || received != total || !bytes.Equal(source.Sum(nil), sink.Sum(nil)) {
				t.Fatalf("incomplete/corrupt: sent=%d received=%d want=%d", sent, received, total)
			}
			if peak > 512<<10 || am.Usage().RXBytes != 0 || pauses == 0 {
				t.Fatalf("peak=%d retained=%d pauses=%d", peak, am.Usage().RXBytes, pauses)
			}
			t.Logf("bytes=%d rate=%d peak_queue=%d pauses=%d", received, rate, peak, pauses)
		})
	}
}
