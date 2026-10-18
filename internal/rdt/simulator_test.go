package rdt

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
)

type delivery struct {
	to    int
	event Event
}
type pair struct {
	t        testing.TB
	owners   [2]*Machine
	clock    *sim.Scheduler[delivery]
	timers   [2]*sim.Timer[delivery]
	network  *sim.Network
	link     sim.Link
	output   [2][]byte
	failures []string
	maxUsage [2]Usage
	read     bool
	trace    []byte
}

func newPair(t testing.TB, seed uint64, l sim.Link, edit func(*Config)) *pair {
	t.Helper()
	p := &pair{t: t, clock: sim.New[delivery](1<<20, 4096), network: sim.NewNetwork(seed, 1<<20, 4096), link: l, read: true}
	for i := range p.owners {
		c := DefaultConfig()
		c.LocalID = byte(7 + 2*i)
		if edit != nil {
			edit(&c)
		}
		m, err := New(c)
		if err != nil {
			t.Fatal(err)
		}
		p.owners[i] = m
		p.timers[i] = sim.NewTimer(p.clock)
	}
	for i := range p.owners {
		p.step(i, Event{Kind: Start}, 0)
	}
	return p
}
func (p *pair) step(i int, e Event, now core.Instant) {
	p.t.Helper()
	aa := p.owners[i].Step(e, now)
	for _, a := range aa {
		switch a.Kind {
		case Send:
			l := p.link
			// Network and owner scheduler advance separately. Preserve the exact send
			// instant by adding its offset from the network's current virtual clock.
			l.Delay += now.Sub(p.network.Scheduler.Now())
			if err := p.network.Send(sim.Packet{From: fmt.Sprint(i), To: fmt.Sprint(1 - i), Data: a.Data}, l); err != nil {
				p.t.Fatal(err)
			}
			p.trace = append(p.trace, a.Data[4])
		case ArmTimer:
			if err := p.timers[i].Arm(a.At, delivery{i, Event{Kind: Tick}}); err != nil {
				p.t.Fatal(err)
			}
		case CancelTimer:
			p.timers[i].Cancel()
		case Readable:
			if p.read {
				if err := p.clock.Schedule(max(now, p.clock.Now()), delivery{i, Event{Kind: Read, Count: 8196}}, 0); err != nil {
					p.t.Fatal(err)
				}
			}
		case Bytes:
			p.output[i] = append(p.output[i], a.Data...)
		case Failed:
			p.failures = append(p.failures, a.Reason)
		}
	}
	u := p.owners[i].Usage()
	old := &p.maxUsage[i]
	old.TXBytes = max(old.TXBytes, u.TXBytes)
	old.TXPackets = max(old.TXPackets, u.TXPackets)
	old.RXBytes = max(old.RXBytes, u.RXBytes)
	old.RXPackets = max(old.RXPackets, u.RXPackets)
	old.RXCapacity = max(old.RXCapacity, u.RXCapacity)
}
func (p *pair) run(until time.Duration) {
	p.t.Helper()
	// Interleave two independently bounded adapters in 1ms quanta. Link delay is
	// >=1ms, and RDT's smallest scheduled interval is 20ms, so timer insertion
	// never occurs behind the owner scheduler. No wall-clock sleeps are involved.
	for p.clock.Now() < core.Instant(until) {
		at := min(core.Instant(until), p.clock.Now().Add(time.Millisecond))
		if err := p.clock.Run(at, 100000, func(d delivery, now core.Instant) error { p.step(d.to, d.event, now); return nil }); err != nil {
			p.t.Fatal(err)
		}
		if err := p.network.Scheduler.Run(at, 100000, func(d sim.Packet, now core.Instant) error {
			i := 0
			if d.To == "1" {
				i = 1
			}
			packet, err := Decode(d.Data)
			if err != nil {
				return err
			}
			p.step(i, Event{Kind: Receive, Packet: packet}, now)
			return nil
		}); err != nil {
			p.t.Fatal(err)
		}
	}
}
func TestSimulatorLossReorderDuplicateMTU(t *testing.T) {
	run := func() []byte {
		p := newPair(t, 1901, sim.Link{MTU: 300, LossPPM: 150000, DuplicatePPM: 250000, Delay: time.Millisecond, Jitter: 30 * time.Millisecond, ConsumerDelay: time.Millisecond}, func(c *Config) { c.MaxPayload = 280; c.InitialWindow = 16; c.MinimumWindow = 8 })
		p.run(2 * time.Second)
		for _, m := range p.owners {
			if m.Status() != Established {
				t.Fatal("creation", m.Status(), p.failures)
			}
		}
		input := bytes.Repeat([]byte("ordered packet bytes\x00"), 1000)
		for offset := 0; offset < len(input); {
			n := min(8196, len(input)-offset)
			p.step(0, Event{Kind: Write, Data: input[offset : offset+n]}, p.clock.Now())
			offset += n
		}
		p.run(25 * time.Second)
		if !bytes.Equal(input, p.output[1]) || p.owners[0].Usage().TXBytes != 0 || len(p.failures) != 0 {
			t.Fatalf("delivered %d/%d, TX=%+v, failures=%v", len(p.output[1]), len(input), p.owners[0].Usage(), p.failures)
		}
		if p.network.Dropped == 0 || p.network.Duplicated == 0 {
			t.Fatal("no impairments", p.network)
		}
		p.step(0, Event{Kind: Close}, p.clock.Now())
		p.run(31 * time.Second)
		if p.owners[0].Status() != Stopped || p.owners[1].Status() != Stopped {
			t.Fatal("close did not finish")
		}
		return p.trace
	}
	if !bytes.Equal(run(), run()) {
		t.Fatal("seed replay changed trace")
	}
}
func TestMTUDropRetainsDataUntilFailure(t *testing.T) {
	p := newPair(t, 3, sim.Link{MTU: 100, Delay: time.Millisecond}, nil)
	p.run(time.Second)
	p.step(0, Event{Kind: Write, Data: bytes.Repeat([]byte{1}, 200)}, p.clock.Now())
	p.run(10 * time.Second)
	if len(p.output[1]) != 0 || p.network.Dropped == 0 || p.owners[0].Usage().TXBytes != 200 {
		t.Fatal("MTU drop lost ownership")
	}
	p.step(0, Event{Kind: Close}, p.clock.Now())
	p.run(16 * time.Second)
	if p.owners[0].Status() != Stopped || p.owners[0].Usage().TXBytes != 0 {
		t.Fatal("bounded shutdown")
	}
}
func TestSimulatorTimerDeadlinesAndClockJumps(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.CreateTimeout = 250 * time.Millisecond })
	s := sim.New[Event](0, 1)
	timer := sim.NewTimer(s)
	var sends int
	handle := func(e Event, now core.Instant) error {
		for _, a := range m.Step(e, now) {
			switch a.Kind {
			case ArmTimer:
				if err := timer.Arm(a.At, Event{Kind: Tick}); err != nil {
					return err
				}
			case CancelTimer:
				timer.Cancel()
			case Send:
				sends++
			}
		}
		return nil
	}
	if err := handle(Event{Kind: Start}, 0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		s.JumpWall(24 * time.Hour)
		if err := handle(Event{Kind: Tick}, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Run(core.Instant(249*time.Millisecond), 100, handle); err != nil {
		t.Fatal(err)
	}
	if sends != 2 || m.Status() != Creating {
		t.Fatal(sends, m.Status())
	}
	if err := s.Run(core.Instant(250*time.Millisecond), 100, handle); err != nil {
		t.Fatal(err)
	}
	if m.Status() != Stopped {
		t.Fatal("exact creation deadline")
	}
	if _, count := s.Usage(); count != 0 || s.PeakCount != 1 {
		t.Fatal("timer accumulated", count, s.PeakCount)
	}
}

// Eight hours of periodic bidirectional traffic, with a receiver deliberately
// retaining a fixed backlog throughout. Pure timer/network adapters keep live
// memory bounded; this is ownership accounting, not a full-process RSS claim.
func TestAcceleratedEightHours(t *testing.T) {
	s := sim.New[delivery](1<<20, 128)
	owners := [2]*Machine{newMachine(t, nil), newMachine(t, func(c *Config) { c.LocalID = 9 })}
	timers := [2]*sim.Timer[delivery]{sim.NewTimer(s), sim.NewTimer(s)}
	var maxRX, maxTX, delivered int
	stopping := false
	handle := func(d delivery, now core.Instant) error {
		for _, a := range owners[d.to].Step(d.event, now) {
			switch a.Kind {
			case ArmTimer:
				if err := timers[d.to].Arm(a.At, delivery{d.to, Event{Kind: Tick}}); err != nil {
					return err
				}
			case CancelTimer:
				timers[d.to].Cancel()
			case Send:
				p, err := Decode(a.Data)
				if err != nil {
					return err
				}
				if err := s.Schedule(now.Add(time.Millisecond), delivery{1 - d.to, Event{Kind: Receive, Packet: p}}, len(p.Payload)); err != nil {
					return err
				}
			case Bytes:
				delivered += len(a.Data)
			case Failed:
				if !stopping {
					return fmt.Errorf("unexpected failure: %s", a.Reason)
				}
			}
		}
		for _, m := range owners {
			u := m.Usage()
			maxRX = max(maxRX, u.RXBytes)
			maxTX = max(maxTX, u.TXBytes)
			if u.RXCapacity != u.RXBytes {
				return fmt.Errorf("retained capacity: %+v", u)
			}
		}
		return nil
	}
	for i := range owners {
		if err := handle(delivery{i, Event{Kind: Start}}, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Run(core.Instant(time.Second), 10000, handle); err != nil {
		t.Fatal(err)
	}
	// Endpoint 1 remains a blocked reader with a constant 4096-byte backlog.
	if err := handle(delivery{0, Event{Kind: Write, Data: make([]byte, 4096)}}, s.Now()); err != nil {
		t.Fatal(err)
	}
	for minute := 1; minute <= 480; minute++ {
		s.JumpWall(-3 * time.Hour)
		if err := handle(delivery{1, Event{Kind: Write, Data: []byte("reply")}}, s.Now()); err != nil {
			t.Fatal(err)
		}
		if err := s.Run(core.Instant(time.Duration(minute)*time.Minute), 10000, handle); err != nil {
			t.Fatal(err)
		}
		if err := handle(delivery{0, Event{Kind: Read, Count: 8196}}, s.Now()); err != nil {
			t.Fatal(err)
		}
		if owners[1].Usage().RXBytes != 4096 {
			t.Fatal("slow-reader backlog changed", owners[1].Usage())
		}
	}
	if delivered != 480*5 || maxRX != 4096 || maxTX > 4096 || s.PeakCount > 16 || s.PeakBytes > 8192 {
		t.Fatal(delivered, maxRX, maxTX, s.PeakCount, s.PeakBytes)
	}
	stopping = true
	for i := range owners {
		if err := handle(delivery{i, Event{Kind: LocalAbort}}, s.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Run(core.Instant(8*time.Hour+time.Second), 10000, func(d delivery, now core.Instant) error { owners[d.to].Step(d.event, now); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, m := range owners {
		if m.Usage() != (Usage{}) {
			t.Fatal(m.Usage())
		}
	}
}
