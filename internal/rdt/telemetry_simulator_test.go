package rdt

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"testing"
	"time"
)

func TestSimulatorTelemetryExactCounts(t *testing.T) {
	for _, tc := range []struct {
		name string
		link sim.Link
	}{
		{"ordered", sim.Link{MTU: 1388, Delay: time.Millisecond}},
		{"reorder_duplicate", sim.Link{MTU: 1388, Delay: time.Millisecond, Jitter: 30 * time.Millisecond, DuplicatePPM: 250000}},
		{"loss", sim.Link{MTU: 1388, Delay: time.Millisecond, LossPPM: 150000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPair(t, 1901, tc.link, func(c *Config) { c.Telemetry = true; c.InitialWindow = 16; c.MinimumWindow = 8 })
			p.run(time.Second)
			var received, duplicates, retries, acks, sacks, delivered [2]uint64
			seen := [2]map[Sequence]bool{{}, {}}
			p.observe = func(i int, e Event, aa []Action, _ core.Instant) {
				if e.Kind == Receive && (e.Packet.Type == Data || e.Packet.Type == Urgent) {
					if seen[i][e.Packet.Sequence] {
						duplicates[i]++
					} else {
						seen[i][e.Packet.Sequence] = true
						received[i] += uint64(len(e.Packet.Payload))
					}
				}
				for _, a := range aa {
					switch a.Kind {
					case Bytes:
						delivered[i] += uint64(len(a.Data))
					case Send:
						wire, err := Decode(a.Data)
						if err != nil {
							t.Fatal(err)
						}
						switch wire.Type {
						case Data, Urgent:
							if wire.Transmissions > 1 {
								retries[i]++
							}
						case DataACK, UrgentACK, ACK2:
							acks[i]++
						case SACK2:
							sacks[i]++
						}
					}
				}
			}
			input := bytes.Repeat([]byte{0x9a}, 8196)
			p.step(0, Event{Kind: Write, Data: input}, p.clock.Now())
			p.run(20 * time.Second)
			if !bytes.Equal(input, p.output[1]) {
				t.Fatalf("delivered %d want %d", len(p.output[1]), len(input))
			}
			for i, m := range p.owners {
				s := m.Stats(p.clock.Now())
				if s.AdmittedBytes != received[i] || s.Duplicates != duplicates[i] || s.RetransmittedSent != retries[i] || s.ACKSent != acks[i] || s.SACKSent != sacks[i] || s.DeliveredBytes != delivered[i] {
					t.Fatalf("owner=%d stats=%+v want admitted=%d duplicate=%d retry=%d ACK=%d SACK=%d delivered=%d", i, s, received[i], duplicates[i], retries[i], acks[i], sacks[i], delivered[i])
				}
			}
		})
	}
}
