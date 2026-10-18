package rdt

import (
	"bytes"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"testing"
	"time"
)

// diagnosisRun composes the actual RDT and tunnel cores. The two tunnel startup
// exchanges are completed independently so the workload begins with matched
// open slots. Every later DATA/control traverses the impaired RDT packet link.
// ConsumerDelay is a relay packet-service cap, not local socket write latency.
type diagnosisProfile struct {
	name         string
	link         sim.Link
	window, high int
	localDelay   time.Duration
}
type diagnosisSignature struct {
	rate                                       uint64
	sender, receiver                           Stats
	tunnel                                     p2ptunnel.ChannelTelemetry
	windowBlocked, unsentMax, flightMax, rxMax int
	deliveryAgeMax                             time.Duration
}

func diagnosisRun(t testing.TB, cfg diagnosisProfile, seed uint64, enabled bool) diagnosisSignature {
	t.Helper()
	p := newPair(t, seed, cfg.link, func(c *Config) {
		c.Telemetry = enabled
		c.InitialWindow = cfg.window
		c.MinimumWindow = 8
		c.TXPackets = 256
		c.RXPackets = 512
	})
	p.run(2 * time.Second)
	var tunnels [2]*p2ptunnel.Machine
	var opened [2]p2ptunnel.Action
	for side := range tunnels {
		c := p2ptunnel.DefaultConfig()
		c.HighWater = cfg.high
		m, err := p2ptunnel.New(c)
		if err != nil {
			t.Fatal(err)
		}
		if enabled {
			m.EnableTelemetry()
		}
		tunnels[side] = m
		aa := m.Step(p2ptunnel.Event{Kind: p2ptunnel.Start}, p.clock.Now())
		var send p2ptunnel.Action
		for _, a := range aa {
			if a.Kind == p2ptunnel.Send {
				send = a
			}
		}
		m.Step(p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: []byte{8, 0, 8, 0, 0, 0, 0, 0, 0, 0, 0, 0}}, p.clock.Now())
		for _, a := range m.Step(p2ptunnel.Event{Kind: p2ptunnel.Open, Port: 4700, Request: 1}, p.clock.Now()) {
			if a.Kind == p2ptunnel.Opened {
				opened[side] = a
			}
		}
		for len(send.Data) > 0 {
			aa = m.Step(p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: send.Token, Count: len(send.Data)}, p.clock.Now())
			send = p2ptunnel.Action{}
			for _, a := range aa {
				if a.Kind == p2ptunnel.Send {
					send = a
				}
			}
		}
	}
	var pending p2ptunnel.Action
	var writeAt core.Instant
	var delivered uint64
	var result diagnosisSignature
	schedule := func(side int, e Event, now core.Instant) {
		if err := p.clock.Schedule(max(now, p.clock.Now()), delivery{side, e}, len(e.Data)); err != nil {
			t.Fatal(err)
		}
	}
	var tunnelStep func(int, p2ptunnel.Event, core.Instant)
	tunnelStep = func(side int, e p2ptunnel.Event, now core.Instant) {
		for _, a := range tunnels[side].Step(e, now) {
			switch a.Kind {
			case p2ptunnel.Send:
				schedule(side, Event{Kind: Write, Token: a.Token, Data: a.Data}, now)
			case p2ptunnel.WriteLocal:
				if side != 1 {
					t.Fatal("unexpected local DATA on sender")
				}
				for _, v := range a.Data {
					if v != 0x9a {
						t.Fatal("corrupt local content")
					}
				}
				pending = a
				writeAt = now.Add(cfg.localDelay)
			case p2ptunnel.ReadLocal:
				if side == 0 {
					tunnelStep(side, p2ptunnel.Event{Kind: p2ptunnel.LocalData, Channel: a.Channel, Generation: a.Generation, Token: a.Token, Data: bytes.Repeat([]byte{0x9a}, 8192)}, now)
				}
			case p2ptunnel.SessionClosed, p2ptunnel.CloseLocal:
				t.Fatalf("unexpected tunnel stop: %s", a.Reason)
			}
		}
	}
	p.observe = func(side int, _ Event, aa []Action, _ core.Instant) {
		for _, a := range aa {
			switch a.Kind {
			case Accepted:
				tunnelStep(side, p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: a.Token, Count: a.Count}, p.owners[side].now)
			case Bytes:
				tunnelStep(side, p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: a.Data}, p.owners[side].now)
			case WriteCredit:
				tunnelStep(side, p2ptunnel.Event{Kind: p2ptunnel.TransportWritable}, p.owners[side].now)
			case QueueStatus:
				tunnelStep(side, p2ptunnel.Event{Kind: p2ptunnel.ReceiveQueueSize, Count: a.Usage.RXBytes}, p.owners[side].now)
			}
		}
	}
	start := p.clock.Now()
	var measured uint64
	// Six virtual minutes; offer 256 KiB/s, bounded by real tunnel read admission.
	for p.clock.Now() < start.Add(6*time.Minute) {
		now := p.clock.Now()
		if p.owners[0].Credit() >= 8196 {
			tunnelStep(0, p2ptunnel.Event{Kind: p2ptunnel.ReadReady, Channel: opened[0].Channel, Generation: opened[0].Generation}, now)
		}
		until := now.Add(32 * time.Millisecond)
		// Service a single local write at its exact deadline, including a zero-delay
		// fast consumer. No worker/sleep, and no retained payload history.
		for p.clock.Now() < until {
			next := min(until, p.clock.Now().Add(time.Millisecond))
			p.run(time.Duration(next))
			for len(pending.Data) > 0 && writeAt <= p.clock.Now() {
				a := pending
				pending = p2ptunnel.Action{}
				delivered += uint64(len(a.Data))
				tunnelStep(1, p2ptunnel.Event{Kind: p2ptunnel.LocalWritten, Channel: a.Channel, Generation: a.Generation, Token: a.Token, Count: len(a.Data)}, p.clock.Now())
			}
			p.output[0] = p.output[0][:0]
			p.output[1] = p.output[1][:0]
			p.trace = p.trace[:0]
		}
		if p.clock.Now() <= start.Add(10*time.Second) {
			measured = delivered
		}
		s := p.owners[0].Stats(p.clock.Now())
		v := tunnels[1].Telemetry(opened[1].Channel, p.clock.Now())
		if s.UnsentPackets > 0 && s.InFlightPackets >= s.CWND {
			result.windowBlocked++
		}
		result.unsentMax = max(result.unsentMax, s.UnsentPackets)
		result.flightMax = max(result.flightMax, s.InFlightPackets)
		result.rxMax = max(result.rxMax, tunnels[1].QueueStats(opened[1].Channel, p.clock.Now()).RXBytes)
		result.deliveryAgeMax = max(result.deliveryAgeMax, v.DeliveryAge)
	}
	if len(p.failures) > 0 {
		t.Fatalf("RDT failures=%v", p.failures)
	}
	result.rate = (delivered - measured) / 350
	result.sender = p.owners[0].Stats(p.clock.Now())
	result.receiver = p.owners[1].Stats(p.clock.Now())
	result.tunnel = tunnels[1].Telemetry(opened[1].Channel, p.clock.Now())
	return result
}

func TestSimulatorTelemetryDiagnosticSignatures(t *testing.T) {
	profiles := []diagnosisProfile{
		{"baseline", sim.Link{MTU: 1388, Delay: 10 * time.Millisecond}, 32, 65536, 0},
		{"service16", sim.Link{MTU: 1388, Delay: 10 * time.Millisecond, ConsumerDelay: 8 * time.Millisecond}, 16, 65536, 0},
		{"service32", sim.Link{MTU: 1388, Delay: 10 * time.Millisecond, ConsumerDelay: 8 * time.Millisecond}, 32, 65536, 0},
		{"rtt16", sim.Link{MTU: 1388, Delay: 100 * time.Millisecond}, 16, 65536, 0},
		{"rtt32", sim.Link{MTU: 1388, Delay: 100 * time.Millisecond}, 32, 65536, 0},
		{"reorder", sim.Link{MTU: 1388, Delay: 10 * time.Millisecond, Jitter: 50 * time.Millisecond}, 32, 65536, 0},
		{"loss", sim.Link{MTU: 1388, Delay: 10 * time.Millisecond, LossPPM: 30000}, 32, 65536, 0},
		{"consumer", sim.Link{MTU: 1388, Delay: 10 * time.Millisecond}, 32, 65536, 100 * time.Millisecond},
	}
	for _, seed := range []uint64{1901, 9011} {
		results := map[string]diagnosisSignature{}
		for _, cfg := range profiles {
			t.Run(fmt.Sprintf("%s/seed%d", cfg.name, seed), func(t *testing.T) {
				s := diagnosisRun(t, cfg, seed, true)
				results[cfg.name] = s
				t.Logf("bytes_per_s=%d cwnd=%d window_blocked_samples=%d unsent_max=%d flight_max=%d rtt=%s SACK_rx=%d ranges_rx=%d retry_SACK=%d retry_RTO=%d reorder_arrivals=%d reorder_max=%d tunnel_pause=%d resume=%d wake=%d paused=%s local_q_max=%d decision_q_max=%d delivery_age_max=%s", s.rate, s.sender.CWND, s.windowBlocked, s.unsentMax, s.flightMax, s.sender.SRTT, s.sender.SACKReceived, s.sender.SACKRangesReceived, s.sender.RetransmitSACK, s.sender.RetransmitRTO, s.receiver.OutOfOrderArrivals, s.receiver.ReorderDistanceMax, s.tunnel.Pause, s.tunnel.Resume, s.tunnel.Wake, s.tunnel.Paused, s.rxMax, s.tunnel.DecisionQMax, s.deliveryAgeMax)
			})
		}
		a, b := results["service16"], results["service32"]
		if a.rate > 0 && (b.rate*100/a.rate < 90 || b.rate*100/a.rate > 110 || b.sender.RetransmittedSent != 0 || b.tunnel.Pause != 0) {
			t.Errorf("cap control not a stable clean plateau: %+v %+v", a, b)
		}
		a, b = results["rtt16"], results["rtt32"]
		if a.rate > 0 && (b.rate < a.rate*17/10 || a.windowBlocked == 0 || b.sender.RetransmittedSent != 0 || b.tunnel.Pause != 0) {
			t.Errorf("RTT/window control signature: %+v %+v", a, b)
		}
		for _, name := range []string{"reorder", "loss"} {
			s, ok := results[name]
			if ok && (s.sender.RetransmitSACK == 0 || s.receiver.OutOfOrderArrivals == 0 || s.sender.SACKRangesReceived == 0) {
				t.Errorf("%s missing storm signature: %+v", name, s)
			}
		}
		s, ok := results["consumer"]
		if ok && (s.tunnel.Pause == 0 || s.tunnel.Paused < time.Minute || s.deliveryAgeMax < 90*time.Millisecond || s.sender.RetransmitSACK != 0 || s.receiver.OutOfOrderArrivals != 0) {
			t.Errorf("consumer signature=%+v", s)
		}
	}
}
