package p2ptunnel

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"math/rand/v2"
	"testing"
	"time"
)

func TestSimulatorPairedLifecycle(t *testing.T) { runPairedLifecycle(t) }

func runPairedLifecycle(t *testing.T) {
	// The tunnel sees RDT's ordered stream; loss/duplication/reordering are tested
	// on sim.Network separately until the RDT machine can supply reliability.
	type delivery struct {
		target int
		event  Event
	}
	scheduler := sim.New[delivery](256*1024, 4096)
	agentCfg := DefaultConfig()
	serverCfg := DefaultConfig()
	serverCfg.Role = Server
	serverCfg.VerifyPorts = true
	serverCfg.AllowedPorts = []uint16{4700}
	agent, _ := New(agentCfg)
	server, _ := New(serverCfg)
	machines := []*Machine{agent, server}
	step := func(side int, e Event, now core.Instant) []Action {
		aa := machines[side].Step(e, now)
		return aa
	}
	timers := []*sim.Timer[delivery]{sim.NewTimer(scheduler), sim.NewTimer(scheduler)}
	var next [2]core.Instant
	var received [2][]byte
	var sockets [2]Action
	var connected [2]bool
	var opened bool
	schedule := func(at core.Instant, target int, e Event) {
		t.Helper()
		if err := scheduler.Schedule(at, delivery{target, e}, len(e.Data)); err != nil {
			t.Fatal(err)
		}
	}
	schedule(0, 0, Event{Kind: Start})
	schedule(0, 1, Event{Kind: Start})
	handle := func(d delivery, now core.Instant) error {
		m := machines[d.target]
		a := step(d.target, d.event, now)
		assertBudget(t, m)
		for _, x := range a {
			switch x.Kind {
			case Send:
				// Fragment every frame down to one byte and delay it. The direction's
				// deadline preserves RDT stream order while the other direction progresses.
				for _, b := range x.Data {
					next[d.target] = max(next[d.target], now).Add(50 * time.Microsecond)
					schedule(next[d.target], 1-d.target, Event{Kind: Stream, Data: []byte{b}})
				}
				schedule(now, d.target, Event{Kind: Sent, Token: x.Token, Count: len(x.Data)})
			case SessionReady:
				if d.target == 0 {
					schedule(now, 0, Event{Kind: Open, Port: 4700, Request: 123})
				}
			case Opened:
				opened = true
				sockets[0] = x
				connected[0] = true
			case ConnectLocal:
				sockets[1] = x
				connected[1] = true
				schedule(now.Add(time.Millisecond), 1, local(Event{Kind: Connected}, x))
			case WriteLocal:
				n := min(3, len(x.Data))
				received[d.target] = append(received[d.target], x.Data[:n]...)
				delay := time.Millisecond
				if d.target == 0 {
					delay = 5 * time.Millisecond
				}
				schedule(now.Add(delay), d.target, local(Event{Kind: LocalWritten, Count: n}, x))
			case ArmTimer:
				if err := timers[d.target].Arm(x.At, delivery{d.target, Event{Kind: Tick}}); err != nil {
					return err
				}
			case CancelTimer:
				timers[d.target].Cancel()
			}
		}
		return nil
	}
	if e := scheduler.Run(core.Instant(time.Second), 10000, handle); e != nil {
		t.Fatal(e)
	}
	if !opened || !connected[1] {
		t.Fatal("setup did not complete")
	}
	for side := range machines {
		now := scheduler.Now()
		a := step(side, local(Event{Kind: ReadReady}, sockets[side]), now)
		r := mustAction(t, a, ReadLocal)
		data := []byte("request")
		if side == 1 {
			data = []byte("response")
		}
		schedule(now, side, local(Event{Kind: LocalData, Data: data}, r))
	}
	if e := scheduler.Run(core.Instant(2*time.Second), 10000, handle); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(received[0], []byte("response")) || !bytes.Equal(received[1], []byte("request")) {
		t.Fatal("byte stream corrupted", received)
	}
	// Simultaneous local EOF, crossing ENDs, no close acknowledgement loop.
	for side := range machines {
		r := mustAction(t, step(side, local(Event{Kind: ReadReady}, sockets[side]), scheduler.Now()), ReadLocal)
		schedule(scheduler.Now(), side, local(Event{Kind: LocalEOF}, r))
	}
	if e := scheduler.Run(core.Instant(3*time.Second), 10000, handle); e != nil {
		t.Fatal(e)
	}
	for _, m := range machines {
		assertBudget(t, m)
		if m.Usage().Channels != 0 || m.Usage().RXBytes != 0 || m.Usage().TXBytes != 0 {
			t.Fatal("teardown leaked", m.Usage())
		}
	}
}

func TestSeededChannelInterleavings(t *testing.T) {
	for seed := uint64(0); seed < 10; seed++ {
		d := activeDriver(t, DefaultConfig(), false)
		rng := rand.New(rand.NewPCG(seed, 42))
		var handles []Action
		for step := 0; step < 2000; step++ {
			d.now += core.Instant(time.Millisecond * 50)
			if len(handles) == 0 || rng.IntN(8) == 0 {
				a := d.step(Event{Kind: Open, Port: 4700})
				if h, ok := find(a, Opened); ok {
					handles = append(handles, h)
				}
				continue
			}
			h := handles[rng.IntN(len(handles))]
			switch rng.IntN(10) {
			case 0:
				d.frame(Frame{DataCommand, h.Channel, bytes.Repeat([]byte{byte(step)}, rng.IntN(128)+1)})
			case 1:
				d.frame(Frame{Command: EndCommand, Channel: h.Channel})
			case 2:
				d.frame(Frame{Command: CloseCommand, Channel: h.Channel})
			case 3:
				d.frame(Frame{Command: PauseCommand, Channel: h.Channel})
			case 4:
				d.frame(Frame{Command: ResumeCommand, Channel: h.Channel})
			case 5:
				d.frame(Frame{Command: WakeCommand, Channel: h.Channel})
			case 6:
				a := d.step(local(Event{Kind: ReadReady}, h))
				if r, ok := find(a, ReadLocal); ok {
					kind := LocalData
					if rng.IntN(4) == 0 {
						kind = LocalEOF
					}
					d.step(local(Event{Kind: kind, Data: []byte{1}}, r))
				}
			case 7:
				d.step(local(Event{Kind: LocalClose}, h))
			case 8:
				d.step(Event{Kind: Tick})
			case 9:
				c := d.m.channels[h.Channel]
				if c.active && c.writeToken != 0 {
					d.step(Event{Kind: LocalWritten, Channel: h.Channel, Generation: c.generation, Token: c.writeToken, Count: rng.IntN(c.writeCount + 1)})
				}
			}
			assertBudget(t, d.m)
			d.actions = nil
			d.wire = nil
		}
		d.step(Event{Kind: Stop})
		if d.m.Usage().RXBytes != 0 || d.m.Usage().Channels != 0 {
			t.Fatal("cleanup")
		}
	}
}
