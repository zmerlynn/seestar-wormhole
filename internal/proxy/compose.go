package proxy

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/edge"
	"io"
	"net"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/dtls"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type layerEvent struct {
	i *iotc.Event
	r *rdt.Event
	t *p2ptunnel.Event
}

func (x *stack) fail(cause *diagnostic.Terminal) {
	if x.failed {
		return
	}
	x.failed = true
	x.pending = nil
	x.openAdmissions = nil
	x.deadlines[3] = 0
	x.terminal.Store(cause)
	// All handles share this stack. One queue owns its terminal notification;
	// attribution travels in the value, never in the selected handle.
	select {
	case x.handles[0].notices <- session.Event{Kind: session.Failed, Layer: cause.Origin(), Err: cause}:
	default:
	}
	for _, ch := range x.phases {
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- cause:
		default:
		}
	}
	for _, c := range x.streams {
		c.finish(cause)
	}
	x.cancel()
}
func (x *stack) enqueue(e layerEvent) {
	// Leave half the fixed inbox for local ownership/completion events. Network
	// datagrams are unreliable: bounded receive saturation is recoverable loss,
	// including authenticated RDT packets, never failure of the shared session.
	if len(x.queue) >= 256 && (e.i != nil && e.i.Kind == iotc.Wire || e.r != nil && e.r.Kind == rdt.Receive) {
		x.ingressDiscards++
		return
	}
	// A blocked edge must not accumulate stale timer work or spin a zero timer.
	for _, old := range x.queue {
		// Bytes and Readable can announce the same remaining stream. One
		// read demand suffices; duplicate demands would grow with a large
		// recovered window even though every individual event is bounded.
		if e.r != nil && e.r.Kind == rdt.Read && old.r != nil && old.r.Kind == rdt.Read && e.r.Count == old.r.Count && e.r.Token == old.r.Token {
			return
		}
		if e.i != nil && e.i.Kind == iotc.Tick && old.i != nil && old.i.Kind == iotc.Tick {
			return
		}
		if e.r != nil && e.r.Kind == rdt.Tick && old.r != nil && old.r.Kind == rdt.Tick {
			return
		}
		if e.t != nil && e.t.Kind == p2ptunnel.Tick && old.t != nil && old.t.Kind == p2ptunnel.Tick {
			return
		}
	}

	if len(x.queue) >= 512 {
		x.failAt("stack", "inbox_capacity")
		return
	}
	x.queue = append(x.queue, e)
}

// drive serializes inter-layer feedback without recursive Step calls. This also
// prevents old timer actions from overwriting deadlines produced by feedback.
func (x *stack) drive(e layerEvent, now core.Instant) {
	if x.ctx.Err() != nil || x.failed {
		return
	}
	x.now = now
	x.expireOpenAdmissions(now)
	x.initDiscardSummary(now)
	if e.i != nil || e.r != nil || e.t != nil {
		x.enqueue(e)
	}
	if x.driving {
		return
	}
	x.driving = true
	defer func() { x.driving = false }()
	nudged := false
	for steps := 0; !x.failed; steps++ {
	flush:
		for len(x.effects) > 0 {
			select {
			case x.actions <- x.effects[0]:
				x.effects[0] = iotc.Action{}
				x.effects = x.effects[1:]
			default:
				break flush
			}
		}

		if steps >= 4096 {
			x.failAt("stack", "step_budget")
			break
		}
		if len(x.queue) == 0 {
			if nudged || x.tunnel == nil {
				break
			}
			nudged = true
			for ch, c := range x.channels {
				if c != nil && x.wantRead[ch] {
					x.enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.ReadReady, Channel: byte(ch), Generation: x.tunnel.Channel(byte(ch)).Generation}})
				}
			}
			if len(x.queue) == 0 {
				break
			}
		}
		index := 0
		if len(x.effects) > 0 {
			// Only IOTC awaits its effect batch. Local closure, stream completions
			// and bounded RDT processing must continue while the wire is stalled.
			for index < len(x.queue) && x.queue[index].i != nil {
				index++
			}
			if index == len(x.queue) {
				goto blocked
			}
		}
		e := x.queue[index]
		copy(x.queue[index:], x.queue[index+1:])
		x.queue[len(x.queue)-1] = layerEvent{}
		x.queue = x.queue[:len(x.queue)-1]
		switch {
		case e.i != nil && x.iotc != nil:
			before := x.iotc.State()
			actions := x.iotc.Step(*e.i, now)
			states := [...]string{"idle", "connecting", "established", "closed"}
			x.transition("rendezvous", states[before], states[x.iotc.State()])
			for _, a := range actions {
				x.iotcAction(a)
			}
		case e.r != nil && x.reliable != nil:
			before := x.reliable.Status()
			actions := x.reliable.Step(*e.r, now)
			states := [...]string{"idle", "creating", "established", "closing", "stopped"}
			x.transition("rdt", states[before], states[x.reliable.Status()])
			for _, a := range actions {
				x.rdtAction(a)
			}
		case e.t != nil && x.tunnel != nil:
			before := x.tunnel.Status()
			actions := x.tunnel.Step(*e.t, now)
			states := [...]string{"idle", "logging_in", "active", "stopped"}
			x.transition("tunnel", states[before], states[x.tunnel.Status()])
			x.tunnelActions(actions)
		}
	}
blocked:
	if len(x.queue) == 0 {
		x.queue = nil
	}
	x.discardSummary(now)
	x.updateTimer(now)
}

func (x *stack) updateTimer(now core.Instant) {
	x.openAdmissionTimer(now)
	next := x.discardNext
	for layer, at := range x.deadlines {
		if at > 0 && !(layer == 0 && len(x.effects) > 0 && at <= now) && (next == 0 || at < next) {
			next = at
		}
	}
	delay := time.Duration(-1)
	if next > 0 {
		delay = max(0, time.Duration(next-now))
	}
	select {
	case <-x.timerUpdates:
	default:
	}
	select {
	case x.timerUpdates <- delay:
	default:
	}
}
func (x *stack) iotcAction(a iotc.Action) {
	switch a.Kind {
	case iotc.RelayProgressChanged:
		x.relayDiagnostic("relay_progress", a.Progress, 0)
	case iotc.Ready:
		x.relayDiagnostic("relay_summary", a.Progress, 0)
		x.pathChosen(a)
		select {
		case x.phases[0] <- nil:
		default:
		}
	case iotc.Received:
		err := x.packets.Deliver(1, a.Data)
		if err != nil && !errors.Is(err, edge.ErrDTLSQueueFull) {
			x.failAt("dtls", "packet_delivery_failed")
		}
	case iotc.Failed:
		if !x.failed {
			x.relayDiagnostic("relay_failure_summary", a.Progress, a.Failures)
		}
		reason := "transport_failed"
		switch {
		case a.Reason == "liveness timeout":
			reason = "liveness_timeout"
		case a.Failures&(iotc.DeviceOffline|iotc.DeviceAsleep) != 0:
			reason = "device_offline"
		case a.Failures&iotc.ConnectionTimeout != 0:
			reason = "connection_timeout"
		case a.Failures&iotc.ServerRefused != 0:
			reason = "server_refused"
		}
		x.failAt("rendezvous", reason)
	case iotc.WriteRejected:
		reason := a.Failures.String()
		if reason == "" {
			reason = "transport_failed"
		}
		x.failAt("rendezvous", reason)
	case iotc.ArmTimer:
		x.deadlines[0] = a.At
	case iotc.CancelTimer:
		x.deadlines[0] = 0
	default:
		// At most one IOTC Step batch is pending. Stop processing input until
		// the worker accepts it; queue fullness never means transport failure.
		x.effects = append(x.effects, a)
	}
}
func (x *stack) rdtAction(a rdt.Action) {
	switch a.Kind {
	case rdt.Send:
		select {
		case x.secureTX <- a.Data:
		default:
			x.failAt("dtls", "send_capacity")
		}
	case rdt.Ready:
		select {
		case x.phases[1] <- nil:
		default:
		}
	case rdt.Accepted:
		x.enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: a.Token, Count: a.Count}})
	case rdt.Bytes:
		if len(a.Data) > 0 {
			x.enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: a.Data}})
			x.enqueue(layerEvent{r: &rdt.Event{Kind: rdt.Read, Count: p2ptunnel.MaxInput}})
		}
	case rdt.Readable:
		if x.tunnel != nil {
			x.enqueue(layerEvent{r: &rdt.Event{Kind: rdt.Read, Count: p2ptunnel.MaxInput}})
		}
	case rdt.WriteCredit:
		x.enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.TransportWritable}})
	case rdt.QueueStatus:
		x.enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.ReceiveQueueSize, Count: a.Usage.RXBytes}})
	case rdt.Failed, rdt.Closed, rdt.End:
		x.failAt("rdt", layerReason(a.Reason))
	case rdt.ArmTimer:
		x.deadlines[1] = a.At
	case rdt.CancelTimer:
		x.deadlines[1] = 0
	}
}

// tunnelActions inspects the stop batch before once-only client completion.
// Effects still execute in core order, sharing one cause and reporting receipt.
func (x *stack) tunnelActions(actions []p2ptunnel.Action) {
	var terminal *diagnostic.Terminal
	var stopReason string
	for _, a := range actions {
		if a.Kind == p2ptunnel.SessionClosed {
			stopReason = a.Reason
			terminal = diagnostic.NewTerminal(session.Tunnel, layerReason(a.Reason), session.ErrTransport)
			break
		}
	}
	for _, a := range actions {
		cause := terminal
		// A peer CLOSE or ordinary completion earlier in the batch keeps its
		// own outcome. Only clients affected by this stop share its terminal.
		if a.Reason != stopReason {
			cause = nil
		}
		x.applyTunnelAction(a, cause)
	}
}

func (x *stack) tunnelAction(a p2ptunnel.Action) { x.tunnelActions([]p2ptunnel.Action{a}) }

func (x *stack) applyTunnelAction(a p2ptunnel.Action, terminal *diagnostic.Terminal) {
	switch a.Kind {
	case p2ptunnel.Send:
		x.enqueue(layerEvent{r: &rdt.Event{Kind: rdt.Write, Data: a.Data, Token: a.Token}})
	case p2ptunnel.SessionReady:
		select {
		case x.phases[2] <- nil:
		default:
		}
	case p2ptunnel.SessionClosed:
		x.fail(terminal)
	case p2ptunnel.Opened:
		delete(x.openAdmissions, a.Request)
		c := x.streams[a.Request]
		if c == nil {
			// The verification transaction still owns a late SETUP after dial cancellation.
			x.enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.LocalClose, Channel: a.Channel, Generation: a.Generation}})
			return
		}
		x.channels[a.Channel] = c
		c.channel = a.Channel
		c.request = func(now core.Instant) {
			x.wantRead[a.Channel] = true
			x.drive(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.ReadReady, Channel: a.Channel, Generation: a.Generation}}, now)
		}
		select {
		case c.result <- nil:
		default:
		}
	case p2ptunnel.OpenFailed:
		if _, waiting := x.openAdmissions[a.Request]; a.Temporary && waiting && x.streams[a.Request] != nil {
			x.pending = append(x.pending, a.Request) // at most pendingOpenLimit admitted clients
			return
		}
		delete(x.openAdmissions, a.Request)
		if c := x.streams[a.Request]; c != nil {
			cause := error(session.ErrTransport)
			if terminal != nil {
				cause = terminal
			}
			c.finish(cause)
			delete(x.streams, a.Request)
			x.f.s.forget(c.id)
		}
	case p2ptunnel.ReadLocal:
		x.wantRead[a.Channel] = false
		if c := x.channels[a.Channel]; c != nil {
			select {
			case c.ready <- a:
			default:
				x.failAt("tunnel", "local_queue_capacity")
			}
		}
	case p2ptunnel.WriteLocal:
		if c := x.channels[a.Channel]; c != nil {
			select {
			case c.rx <- delivery{action: a}:
			default:
				x.failAt("tunnel", "local_queue_capacity")
			}
		}
	case p2ptunnel.ShutdownWrite:
		if c := x.channels[a.Channel]; c != nil {
			c.endOnce.Do(func() { close(c.readEnd) })
		}
	case p2ptunnel.CloseLocal:
		if a.Rule != "" && x.f.s.logger != nil {
			x.f.s.logger.Warn("tunnel_refused", "rule", a.Rule, "channel", int(a.Channel), "discarded_bytes", a.DiscardedBytes)
		}
		if c := x.channels[a.Channel]; c != nil {
			cause := error(session.ErrTransport)
			if terminal != nil {
				cause = terminal
			}
			if a.Err != nil {
				cause = a.Err
			}
			switch a.Reason {
			case "peer close":
				cause = peerCloseCause(a.PeerClose)
			case "both directions ended", "local EOF after peer end":
				cause = io.EOF
			case "local close":
				cause = net.ErrClosed
			}
			x.channelClosed(a, c, cause)
			if abnormalChannel(cause) && terminal == nil {
				c.finish(&channelFailure{cause})
			} else {
				c.finish(cause)
			}
			delete(x.streams, c.id)
			x.channels[a.Channel] = nil
			x.f.s.forget(c.id)
		}
	case p2ptunnel.ArmTimer:
		x.deadlines[2] = a.At
	case p2ptunnel.CancelTimer:
		x.deadlines[2] = 0
	}
}
func (x *stack) timers() {
	t := time.NewTimer(time.Hour)
	t.Stop()
	defer t.Stop()
	var tick <-chan time.Time
	for {
		select {
		case <-x.ctx.Done():
			return
		case d := <-x.timerUpdates:
			t.Stop()
			tick = nil
			if d >= 0 {
				t.Reset(d)
				tick = t.C
			}
		case <-tick:
			tick = nil
			if !x.post(func(now core.Instant) {
				if x.discardOnlyTick(now) {
					return
				}
				x.drive(layerEvent{i: &iotc.Event{Kind: iotc.Tick, Attempt: x.id}}, now)
				x.drive(layerEvent{r: &rdt.Event{Kind: rdt.Tick}}, now)
				x.drive(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Tick}}, now)
				if x.deadlines[3] != 0 && now >= x.deadlines[3] {
					pending := x.pending
					x.pending = nil
					x.deadlines[3] = 0
					for _, id := range pending {
						if c := x.streams[id]; c != nil {
							select {
							case <-c.done:
								(&layerHandle{x: x}).TryClose(id)
								continue
							default:
							}
							x.drive(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Open, Request: id, Port: c.port}}, now)
						}
					}
					x.updateTimer(now)
				}
			}) {
				return
			}
		}
	}
}
func (x *stack) secureRead() {
	b := make([]byte, dtls.MaxApplication)
	for {
		n, err := x.secure.Read(b)
		if err != nil {
			x.post(func(core.Instant) { x.failAt("dtls", "read_failed") })
			return
		}
		p, err := rdt.Decode(b[:n])
		if err != nil {
			x.post(func(core.Instant) { x.failAt("rdt", "invalid_frame") })
			return
		}
		// Decode owns its payload, so the next secure read may reuse b.
		e := rdt.Event{Kind: rdt.Receive, Packet: p}
		if !x.post(func(now core.Instant) { x.drive(layerEvent{r: &e}, now) }) {
			return
		}
	}
}
func (x *stack) secureWrite() {
	for {
		select {
		case <-x.ctx.Done():
			return
		case b := <-x.secureTX:
			if n, err := x.secure.Write(b); err != nil || n != len(b) {
				x.post(func(core.Instant) { x.failAt("dtls", "write_failed") })
				return
			}
			if !x.post(func(now core.Instant) {
				x.drive(layerEvent{r: &rdt.Event{Kind: rdt.SendComplete}}, now)
			}) {
				return
			}
		}
	}
}
