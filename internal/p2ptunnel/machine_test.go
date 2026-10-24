package p2ptunnel

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/sim"
	"testing"
	"time"
)

type driver struct {
	observeTurn func(Event, []Action)
	t           *testing.T
	m           *Machine
	now         core.Instant
	wire        []Frame
	actions     []Action
	auto        bool
	observeSend func([]byte)
	scheduler   *sim.Scheduler[Event]
	timer       *sim.Timer[Event]
}

func newDriver(t *testing.T, c Config) *driver {
	t.Helper()
	m, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	scheduler := sim.New[Event](64, 2)
	return &driver{t: t, m: m, auto: true, scheduler: scheduler, timer: sim.NewTimer(scheduler)}
}
func (d *driver) step(e Event) []Action {
	d.t.Helper()
	var out []Action
	events := []Event{e}
	for len(events) > 0 {
		e = events[0]
		events = events[1:]
		a := d.m.Step(e, d.now)
		if d.observeTurn != nil {
			d.observeTurn(e, a)
		}
		out = append(out, a...)
		for _, x := range a {
			switch x.Kind {
			case ArmTimer:
				if err := d.timer.Arm(x.At, Event{Kind: Tick}); err != nil {
					d.t.Fatal(err)
				}
			case CancelTimer:
				d.timer.Cancel()
			}
			if x.Kind == Send && d.auto {
				if d.observeSend != nil {
					d.observeSend(x.Data)
				}
				phase := Established
				if len(x.Data) > 0 && x.Data[0] == 7 && d.m.Status() == LoggingIn {
					phase = LoginRequest
				}
				if len(x.Data) > 0 && (x.Data[0] == 8 || x.Data[0] == 9) {
					phase = LoginResponse
				}
				f, err := Decode(x.Data, phase)
				if err != nil {
					d.t.Fatal("wire", err)
				}
				d.wire = append(d.wire, f)
				events = append(events, Event{Kind: Sent, Token: x.Token, Count: len(x.Data)})
			}
		}
		assertBudget(d.t, d.m)
	}
	d.actions = out
	return out
}
func assertBudget(t testing.TB, m *Machine) {
	t.Helper()
	u := m.Usage()
	if u.Channels < 0 || u.Channels > m.cfg.MaxChannels || u.RXBytes < 0 || u.RXBytes > m.cfg.SessionBytes || u.RXCapacity != u.RXBytes || u.TXBytes < 0 || u.TXBytes+u.ReservedBytes > m.cfg.TXBytes || u.TXFrames+u.ReservedFrames > m.cfg.TXFrames || u.ReservedBytes < 0 || u.ReservedFrames < 0 || u.ParserBytes > MaxInput || u.ControlFrames < 0 || u.ControlFrames > 257 || u.ControlBytes != HeaderSize*u.ControlFrames {
		t.Fatalf("budget: %+v", u)
	}
	sum := 0
	count := 0
	for _, c := range m.channels {
		if c.queue.size > m.cfg.ChannelBytes {
			t.Fatal("channel cap")
		}
		sum += c.queue.size
		if c.active {
			count++
		}
	}
	if sum != u.RXBytes || count != u.Channels {
		t.Fatal("accounting")
	}
}
func find(a []Action, k ActionKind) (Action, bool) {
	for _, x := range a {
		if x.Kind == k {
			return x, true
		}
	}
	return Action{}, false
}
func mustAction(t *testing.T, a []Action, k ActionKind) Action {
	t.Helper()
	x, ok := find(a, k)
	if !ok {
		t.Fatalf("missing action %d: %+v", k, a)
	}
	return x
}
func (d *driver) frame(f Frame) []Action {
	b, e := f.Encode(Established)
	if e != nil {
		d.t.Fatal(e)
	}
	return d.step(Event{Kind: Stream, Data: b})
}
func activeDriver(t *testing.T, c Config, verify bool) *driver {
	d := newDriver(t, c)
	d.step(Event{Kind: Start})
	r := LoginResult{}
	if verify {
		r.PortVerify = 1
	}
	b, _ := (Frame{Command: PassCommand, Payload: r.Encode()}).Encode(LoginResponse)
	d.step(Event{Kind: Stream, Data: b})
	if d.m.Status() != Active {
		t.Fatal("not active")
	}
	return d
}
func (d *driver) open(port uint16) Action {
	return mustAction(d.t, d.step(Event{Kind: Open, Port: port, Request: uint64(port)}), Opened)
}
func local(e Event, a Action) Event {
	e.Channel = a.Channel
	e.Generation = a.Generation
	e.Token = a.Token
	return e
}
func (d *driver) eof(a Action) {
	read := mustAction(d.t, d.step(local(Event{Kind: ReadReady}, a)), ReadLocal)
	d.step(local(Event{Kind: LocalEOF}, read))
}
func (d *driver) hasWire(cmd Command) bool {
	for _, f := range d.wire {
		if f.Command == cmd {
			return true
		}
	}
	return false
}

func TestLoginAndDeadlines(t *testing.T) {
	c := DefaultConfig()
	d := newDriver(t, c)
	a := d.step(Event{Kind: Start})
	mustAction(t, a, ArmTimer)
	b, _ := (Frame{Command: PassCommand, Payload: (LoginResult{}).Encode()}).Encode(LoginResponse)
	d.step(Event{Kind: Stream, Data: b[:5]})
	if d.m.Status() != LoggingIn {
		t.Fatal("fragment completed early")
	}
	d.step(Event{Kind: Stream, Data: b[5:]})
	if d.m.Status() != Active {
		t.Fatal("login")
	}
	for _, reject := range []bool{true, false} {
		d = newDriver(t, c)
		d.step(Event{Kind: Start})
		if reject {
			b, _ := (Frame{Command: RejectCommand, Payload: (LoginResult{Result: -1}).Encode()}).Encode(LoginResponse)
			d.step(Event{Kind: Stream, Data: b})
		} else {
			d.now = core.Instant(30 * time.Second)
			d.step(Event{Kind: Tick})
		}
		if d.m.Status() != Stopped {
			t.Fatal("expected stop")
		}
		assertBudget(t, d.m)
	}
}
func TestPortVerification(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), true)
	d.step(Event{Kind: Open, Port: 4700, Request: 7})
	p, _ := ParsePort(d.wire[len(d.wire)-1].Payload)
	if p.ID == 0 || p.Port != 4700 {
		t.Fatal(p)
	}
	mustAction(t, d.step(Event{Kind: Open, Port: 80, Request: 8}), OpenFailed)
	d.frame(Frame{Command: PortResponse, Payload: (PortCheck{ID: p.ID + 1, Port: 4700, Valid: 1}).Encode()})
	if d.m.pending == nil {
		t.Fatal("stale response completed verification")
	}
	a := mustAction(t, d.frame(Frame{Command: PortResponse, Payload: (PortCheck{ID: p.ID, Port: 4700, Valid: 1}).Encode()}), Opened)
	if a.Request != 7 {
		t.Fatal(a)
	}
	d.step(Event{Kind: Open, Port: 80, Request: 9})
	p, _ = ParsePort(d.wire[len(d.wire)-1].Payload)
	mustAction(t, d.frame(Frame{Command: PortResponse, Payload: (PortCheck{ID: p.ID, Port: 81, Valid: 1}).Encode()}), OpenFailed)
	d.step(Event{Kind: Open, Port: 80, Request: 10})
	d.now = core.Instant(5 * time.Second)
	mustAction(t, d.step(Event{Kind: Tick}), OpenFailed)
}
func TestHalfCloseDrainAndPartialWrite(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	ch := d.open(4700)
	write := mustAction(t, d.frame(Frame{DataCommand, ch.Channel, []byte("abcdefg")}), WriteLocal)
	a := d.frame(Frame{Command: EndCommand, Channel: ch.Channel})
	if _, ok := find(a, ShutdownWrite); ok {
		t.Fatal("premature EOF")
	}
	write = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: 3}, write)), WriteLocal)
	if string(write.Data) != "defg" {
		t.Fatal("partial write lost bytes")
	}
	mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: 4}, write)), ShutdownWrite)
	d.wire = nil
	d.eof(ch)
	if d.m.Channel(ch.Channel).Active || !d.hasWire(CloseCommand) {
		t.Fatal("EOF after W must CLOSE")
	}
	// Reverse ordering: local END then peer END releases without close ACK.
	ch = d.open(80)
	d.eof(ch)
	d.wire = nil
	mustAction(t, d.frame(Frame{Command: EndCommand, Channel: ch.Channel}), CloseLocal)
	if d.hasWire(CloseCommand) {
		t.Fatal("unexpected close ACK")
	}
}
func TestCloseDropsQueueAndTemporaryPressure(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	ch := d.open(4700)
	w := mustAction(t, d.frame(Frame{DataCommand, ch.Channel, []byte("data")}), WriteLocal)
	d.step(local(Event{Kind: LocalWritten, Count: 0}, w))
	if d.m.Usage().RXBytes != 4 {
		t.Fatal("would block lost bytes")
	}
	w = mustAction(t, d.step(local(Event{Kind: LocalWritable}, ch)), WriteLocal)
	d.frame(Frame{Command: CloseCommand, Channel: ch.Channel})
	d.step(local(Event{Kind: LocalWritten, Count: 4}, w))
	if d.m.Usage().RXBytes != 0 {
		t.Fatal("close did not free")
	}
	// A terminal write error closes the failed channel before later DATA.
	ch = d.open(80)
	w = mustAction(t, d.frame(Frame{DataCommand, ch.Channel, []byte("x")}), WriteLocal)
	d.step(local(Event{Kind: LocalWriteFailed}, w))
	d.frame(Frame{DataCommand, ch.Channel, []byte("ignored")})
	if d.m.Usage().RXBytes != 0 || d.m.Channel(ch.Channel).Active {
		t.Fatal("write failure")
	}
}
func TestPauseResumeWakeCounter(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	ch := d.open(4700)
	d.frame(Frame{Command: PauseCommand, Channel: ch.Channel})
	if d.m.Channel(ch.Channel).ReadState != 2 {
		t.Fatal("pause")
	}
	r := mustAction(t, d.step(local(Event{Kind: ReadReady}, ch)), ReadLocal)
	d.step(local(Event{Kind: LocalData, Data: []byte("x")}, r))
	if _, ok := find(d.step(local(Event{Kind: ReadReady}, ch)), ReadLocal); ok {
		t.Fatal("read beyond opportunity")
	}
	d.frame(Frame{Command: PauseCommand, Channel: ch.Channel})
	if d.m.Channel(ch.Channel).ReadState != 5 {
		t.Fatal("duplicate pause treated as boolean")
	}
	d.frame(Frame{Command: WakeCommand, Channel: ch.Channel})
	if d.m.Channel(ch.Channel).ReadState != 2 {
		t.Fatal("wake")
	}
	r = mustAction(t, d.step(local(Event{Kind: ReadReady}, ch)), ReadLocal)
	d.step(local(Event{Kind: LocalData}, r))
	d.frame(Frame{Command: ResumeCommand, Channel: ch.Channel})
	if d.m.Channel(ch.Channel).ReadState != 0 {
		t.Fatal("resume")
	}
	for i := 0; i < 128; i++ {
		d.frame(Frame{Command: PauseCommand, Channel: ch.Channel})
	}
	if d.m.Channel(ch.Channel).ReadState != 0 {
		t.Fatal("native byte wrap")
	}
}
func TestWatermarksAndWake(t *testing.T) {
	c := DefaultConfig()
	c.HighWater = 10000
	d := activeDriver(t, c, false)
	ch := d.open(4700)
	w := mustAction(t, d.frame(Frame{DataCommand, ch.Channel, make([]byte, 8192)}), WriteLocal)
	d.frame(Frame{DataCommand, ch.Channel, make([]byte, 1808)})
	if d.m.Channel(ch.Channel).SentPause {
		t.Fatal("high equality")
	}
	d.frame(Frame{DataCommand, ch.Channel, []byte{0}})
	if !d.hasWire(PauseCommand) {
		t.Fatal("no pause")
	}
	w = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: 2001}, w)), WriteLocal)
	if !d.m.Channel(ch.Channel).SentPause {
		t.Fatal("low equality")
	}
	d.step(local(Event{Kind: LocalWritten, Count: 1}, w))
	if d.m.Channel(ch.Channel).SentPause || !d.hasWire(ResumeCommand) {
		t.Fatal("no resume")
	}
	// WAKE after more than 8196 bytes across a sender scan, while still above low.
	d = activeDriver(t, c, false)
	ch = d.open(80)
	w = mustAction(t, d.frame(Frame{DataCommand, ch.Channel, make([]byte, 8192)}), WriteLocal)
	for i := 0; i < 3; i++ {
		d.frame(Frame{DataCommand, ch.Channel, make([]byte, 8192)})
	}
	d.wire = nil
	w = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: 8192}, w)), WriteLocal)
	d.step(local(Event{Kind: LocalWritten, Count: 5}, w))
	if !d.hasWire(WakeCommand) || !d.m.Channel(ch.Channel).SentPause {
		t.Fatal("wake semantics")
	}
}
func TestChannelAndSessionBudgets(t *testing.T) {
	for _, sessionLimit := range []bool{false, true} {
		c := DefaultConfig()
		c.ChannelBytes = 8192
		c.HighWater = 4096
		if sessionLimit {
			c.ChannelBytes = 16384
			c.SessionBytes = 16384
		}
		d := activeDriver(t, c, false)
		a := d.open(4700)
		b := d.open(80)
		d.frame(Frame{DataCommand, a.Channel, make([]byte, 8192)})
		if sessionLimit {
			d.frame(Frame{DataCommand, b.Channel, make([]byte, 8192)})
			d.frame(Frame{DataCommand, b.Channel, []byte{1}})
			if !d.m.Channel(b.Channel).Active || d.m.Channel(a.Channel).Active {
				t.Fatal("session overflow isolation")
			}
		} else {
			d.frame(Frame{DataCommand, a.Channel, []byte{1}})
			if d.m.Channel(a.Channel).Active || !d.m.Channel(b.Channel).Active {
				t.Fatal("channel overflow isolation")
			}
			mustAction(t, d.frame(Frame{DataCommand, b.Channel, []byte("ok")}), WriteLocal)
		}
		if d.m.Status() != Active {
			t.Fatal("slow consumer killed siblings")
		}
		d.step(Event{Kind: Stop})
		if d.m.Usage().RXBytes != 0 || d.m.Usage().TXBytes != 0 {
			t.Fatal("cleanup")
		}
	}
}
func TestQuarantineAndStaleCompletions(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	var opened []Action
	for i := 0; i < 256; i++ {
		opened = append(opened, d.open(uint16(i+1)))
	}
	mustAction(t, d.step(Event{Kind: Open, Port: 4700}), OpenFailed)
	for _, a := range opened {
		d.step(local(Event{Kind: LocalClose}, a))
	}
	mustAction(t, d.step(Event{Kind: Open, Port: 4700}), OpenFailed)
	d.now = core.Instant(28 * time.Second)
	mustAction(t, d.step(Event{Kind: Open, Port: 4700}), OpenFailed)
	d.now = core.Instant(5 * time.Minute)
	fresh := d.open(4700)
	if fresh.Channel != opened[0].Channel || fresh.Generation == opened[0].Generation {
		t.Fatal("reuse")
	}
	d.step(local(Event{Kind: LocalClose}, opened[0]))
	if !d.m.Channel(fresh.Channel).Active {
		t.Fatal("stale close")
	}
	d.now = 0
	d.step(local(Event{Kind: LocalClose}, fresh))
	if d.m.Channel(fresh.Channel).FreeAt < core.Instant(10*time.Minute) {
		t.Fatal("backward clock shortened quarantine")
	}
}

func TestChannelReuseFloor(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	old := d.open(4700)
	d.now = core.Instant(1750 * time.Millisecond)
	d.step(local(Event{Kind: LocalClose}, old))
	at := d.now.Add(5 * time.Minute)
	for i, now := range []core.Instant{core.Instant(31 * time.Second), at - 1, at} {
		d.now = now
		fresh := mustAction(t, d.step(Event{Kind: Open, Mapping: byte(i + 1), Port: 4700}), Opened)
		if wantReuse := now == at; (fresh.Channel == old.Channel) != wantReuse {
			t.Fatalf("at %d: channel=%d old=%d want reuse=%v", now, fresh.Channel, old.Channel, wantReuse)
		}
		if fresh.Channel == old.Channel && fresh.Generation == old.Generation {
			t.Fatal("reused channel retained generation")
		}
	}
}

func TestChannelReuseFloorExhaustion(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	var opened []Action
	for range 256 {
		opened = append(opened, d.open(4700))
	}
	d.now = core.Instant(time.Second)
	d.step(local(Event{Kind: LocalClose}, opened[0]))
	at := d.now.Add(5 * time.Minute)
	d.now = core.Instant(2 * time.Second)
	for _, a := range opened[1:] {
		d.step(local(Event{Kind: LocalClose}, a))
	}
	for _, now := range []core.Instant{core.Instant(31 * time.Second), at - 1} {
		d.now = now
		actions := d.step(Event{Kind: Open, Port: 4700, Request: 7})
		failed := mustAction(t, actions, OpenFailed)
		if !failed.Temporary || failed.Request != 7 || failed.Reason != "no free channel (reuse quarantine)" {
			t.Fatalf("quarantine refusal: %+v", failed)
		}
		if _, ok := find(actions, Opened); ok {
			t.Fatal("opened before reuse floor")
		}
	}
	d.now = at
	fresh := d.open(4700)
	if fresh.Channel != opened[0].Channel || fresh.Generation == opened[0].Generation {
		t.Fatalf("reuse after floor: %+v", fresh)
	}
}
func TestOutboundCompletionAndBackpressure(t *testing.T) {
	c := DefaultConfig()
	c.TXBytes = 2 * MaxInput
	d := activeDriver(t, c, false)
	ch := d.open(4700)
	d.auto = false
	r := mustAction(t, d.step(local(Event{Kind: ReadReady}, ch)), ReadLocal)
	send := mustAction(t, d.step(local(Event{Kind: LocalData, Data: make([]byte, 8192)}, r)), Send)
	if _, ok := find(d.step(local(Event{Kind: ReadReady}, ch)), ReadLocal); ok {
		t.Fatal("unbounded read reservation")
	}
	out := mustAction(t, d.step(Event{Kind: Sent, Token: send.Token, Count: 3}), Send)
	if !bytes.Equal(out.Data, send.Data[3:]) {
		t.Fatal("partial RDT write")
	}
	d.step(Event{Kind: Sent, Token: out.Token, Count: 0})
	if _, ok := find(d.step(Event{Kind: Tick}), Send); ok {
		t.Fatal("write spin")
	}
	out = mustAction(t, d.step(Event{Kind: TransportWritable}), Send)
	d.step(Event{Kind: Sent, Token: out.Token, Failed: true})
	if d.m.Status() != Stopped || d.m.Usage().Channels != 0 {
		t.Fatal("failed write cleanup")
	}
}
func TestServerSetupAndRejection(t *testing.T) {
	c := DefaultConfig()
	c.Role = Server
	c.AllowedPorts = []uint16{4700}
	d := newDriver(t, c)
	d.step(Event{Kind: Start})
	b, _ := (Frame{Command: LoginCommand, Payload: unhex(t, "0008030400000000")}).Encode(LoginRequest)
	d.step(Event{Kind: Stream, Data: b})
	setup, _ := (Setup{Port: 80, Address: [4]byte{127, 0, 0, 1}}).Encode()
	d.frame(Frame{SetupCommand, 27, setup})
	f := d.wire[len(d.wire)-1]
	if f.Command != CloseCommand || f.Channel != 27 || f.Payload[0] != 2 {
		t.Fatal("rejection index")
	}
	setup, _ = (Setup{Port: 4700, Address: [4]byte{127, 0, 0, 1}}).Encode()
	connect := mustAction(t, d.frame(Frame{SetupCommand, 27, setup}), ConnectLocal)
	if _, ok := find(d.frame(Frame{DataCommand, 27, []byte("x")}), WriteLocal); ok {
		t.Fatal("write before connect")
	}
	mustAction(t, d.step(local(Event{Kind: Connected}, connect)), WriteLocal)
	next := mustAction(t, d.frame(Frame{SetupCommand, 27, setup}), ConnectLocal)
	if next.Generation == connect.Generation {
		t.Fatal("generation not replaced")
	}
	d.step(local(Event{Kind: Connected, Failed: true}, connect))
	if !d.m.Channel(27).Active {
		t.Fatal("stale connect")
	}
	d.step(local(Event{Kind: Connected, Failed: true}, next))
	if d.m.Channel(27).Active {
		t.Fatal("connect failure")
	}
}

func TestSimulatorEightHoursPlateau(t *testing.T) {
	// A slow channel stays blocked all night; a sibling opens/transfers/half-closes
	// once per simulated minute. Wall jumps do not affect monotonic quarantine.
	d := activeDriver(t, DefaultConfig(), false)
	slow := d.open(445)
	d.frame(Frame{DataCommand, slow.Channel, make([]byte, 8192)})
	scheduler := d.scheduler
	peak := d.m.Usage().RXBytes
	for minute := 0; minute < 8*60; minute++ {
		at := core.Instant(time.Duration(minute+1) * time.Minute)
		if minute%2 == 0 {
			scheduler.JumpWall(-3 * time.Hour)
		} else {
			scheduler.JumpWall(7 * time.Hour)
		}
		err := scheduler.Run(at, 2, func(e Event, now core.Instant) error {
			d.now = now
			d.step(e)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		d.now = at
		ch := d.open(4700)
		w := mustAction(t, d.frame(Frame{DataCommand, ch.Channel, []byte("reply")}), WriteLocal)
		d.frame(Frame{Command: EndCommand, Channel: ch.Channel})
		d.step(local(Event{Kind: LocalWritten, Count: 5}, w))
		d.eof(ch)
		u := d.m.Usage()
		peak = max(peak, u.RXBytes)
		if u.Channels != 1 || u.RXBytes != 8192 || u.TXBytes != 0 || u.ReservedBytes != 0 {
			t.Fatalf("minute %d plateau: %+v", minute, u)
		}
		d.wire = nil
		d.actions = nil // The observer also has bounded retention.
	}
	if peak != 8192 || scheduler.PeakCount > 1 {
		t.Fatal("growth")
	}
	d.step(Event{Kind: Stop})
	if d.m.Usage().Channels != 0 || d.m.Usage().RXBytes != 0 {
		t.Fatal("shutdown")
	}
}

func TestSharedRDTReceivePressure(t *testing.T) {
	c := DefaultConfig()
	c.HighWater = 8192
	d := activeDriver(t, c, false)
	ch := d.open(4700)
	d.step(Event{Kind: ReceiveQueueSize, Count: 8192})
	w := mustAction(t, d.frame(Frame{DataCommand, ch.Channel, []byte{1}}), WriteLocal)
	if !d.hasWire(PauseCommand) {
		t.Fatal("shared RDT bytes not counted")
	}
	d.step(Event{Kind: ReceiveQueueSize, Count: 0})
	d.step(local(Event{Kind: LocalWritten, Count: 1}, w))
	if !d.hasWire(ResumeCommand) {
		t.Fatal("RDT pressure did not clear")
	}
}

func TestStoppedOpenAndMalformedCleanup(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	a := d.open(4700)
	d.frame(Frame{DataCommand, a.Channel, []byte("queued")})
	d.step(Event{Kind: Stream, Data: []byte{2, 1, 255, 255}})
	if d.m.Status() != Stopped || d.m.Usage().RXBytes != 0 || d.m.Usage().Channels != 0 {
		t.Fatal("malformed cleanup")
	}
	mustAction(t, d.step(Event{Kind: Open, Port: 4700, Request: 99}), OpenFailed)
}
