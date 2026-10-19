package rdt

import (
	"bytes"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

func TestReceiveACKLiteralAndFallback(t *testing.T) {
	m := newMachine(t, nil)
	ready(t, m)
	a := m.Step(incoming(Data, 2, "c"), 0)
	if len(packets(t, a)) != 0 {
		t.Fatal("first selective run sent early")
	}
	a = m.Step(Event{Kind: Tick}, core.Instant(100*time.Millisecond))
	pp := packets(t, a)
	if len(pp) != 1 || pp[0].Type != SACK2 {
		t.Fatal(pp)
	}
	b, _ := pp[0].Encode()
	want := unhex(t, "5a97c2f145054800000000000000000001090000000000000100000002000000000000000200000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000")
	if !bytes.Equal(b, want) {
		t.Fatalf("SACK=%x want %x", b, want)
	}
	m.Step(incoming(Data, 0, "a"), core.Instant(101*time.Millisecond))
	a = m.Step(incoming(Data, 1, "b"), core.Instant(104*time.Millisecond))
	pp = packets(t, a)
	if len(pp) != 1 || pp[0].Type != ACK2 || pp[0].Sequence.Low != 2 {
		t.Fatal("gap completion ACK", pp)
	}
	a = m.Step(incoming(Data, 1, "b"), core.Instant(105*time.Millisecond))
	pp = packets(t, a)
	if len(pp) != 1 || pp[0].Type != ACK2 || pp[0].Sequence.Low != 2 {
		t.Fatal("duplicate ACK", pp)
	}
}
func TestPeerVersionAndUrgent(t *testing.T) {
	m := newMachine(t, nil)
	m.Step(Event{Kind: Start}, 0)
	m.Step(Event{Kind: Receive, Packet: Packet{Type: Heartbeat, Version: 4, ID: 9}}, 0)
	m.Step(Event{Kind: Receive, Packet: Packet{Type: HeartbeatACK, Version: 5, ID: 7}}, 0)
	for _, tt := range []struct {
		typ, want Type
		seq       uint32
	}{{Data, DataACK, 0}, {Urgent, UrgentACK, 1}} {
		pp := packets(t, m.Step(incoming(tt.typ, tt.seq, "x"), 0))
		if len(pp) != 1 || pp[0].Type != tt.want {
			t.Fatal(pp)
		}
	}
	// DATA version must not override the heartbeat advertisement.
	if m.peerVersion != 4 {
		t.Fatal(m.peerVersion)
	}
}
func TestRolloverOrderedStream(t *testing.T) {
	m := newMachine(t, nil)
	ready(t, m)
	start := Sequence{0xefffffff, 11}
	m.nextTX = start
	m.nextRead = start
	m.ackNext = start
	for _, s := range []Sequence{{0, 12}, {0xf0000000, 11}, {0xefffffff, 11}} {
		e := incoming(Data, 0, string([]byte{byte(s.Low)}))
		e.Packet.Sequence = s
		m.Step(e, 0)
	}
	a := m.Step(Event{Kind: Read, Count: 100}, 0)
	var got []byte
	for _, v := range a {
		if v.Kind == Bytes {
			got = v.Data
		}
	}
	if !bytes.Equal(got, []byte{255, 0, 0}) || m.nextRead != (Sequence{1, 12}) || m.ackNext != (Sequence{1, 12}) {
		t.Fatalf("read %x next %+v ack %+v", got, m.nextRead, m.ackNext)
	}
	for i := 0; i < 3; i++ {
		pp := packets(t, m.Step(Event{Kind: Write, Data: []byte{1}}, 0))
		if len(pp) != 1 || pp[0].Sequence != start {
			t.Fatal(pp, start)
		}
		start = start.Next()
	}
}
func TestRTORetransmitAndKarn(t *testing.T) {
	m := newMachine(t, nil)
	ready(t, m)
	m.Step(Event{Kind: Write, Data: []byte("x")}, 0)
	pp := packets(t, m.Step(Event{Kind: Tick}, core.Instant(499*time.Millisecond)))
	if len(pp) != 0 {
		t.Fatal("early retransmission", pp)
	}
	pp = packets(t, m.Step(Event{Kind: Tick}, core.Instant(500*time.Millisecond)))
	if len(pp) != 1 || pp[0].Transmissions != 2 || m.RTO() != time.Second {
		t.Fatal(pp, m.RTO())
	}
	m.Step(incoming(ACK2, 0, ""), core.Instant(600*time.Millisecond))
	if m.RTO() != time.Second {
		t.Fatal("ambiguous RTT sampled")
	}
	m.Step(Event{Kind: Write, Data: []byte("y")}, core.Instant(600*time.Millisecond))
	m.Step(incoming(ACK2, 1, ""), core.Instant(700*time.Millisecond))
	if m.RTO() != 300*time.Millisecond {
		t.Fatal("first RTT", m.RTO())
	}
	m.Step(Event{Kind: Write, Data: []byte("z")}, core.Instant(700*time.Millisecond))
	m.Step(incoming(ACK2, 2, ""), core.Instant(900*time.Millisecond))
	if m.RTO() != 362500*time.Microsecond {
		t.Fatal("second RTT", m.RTO())
	}
}
func TestACKBoundariesForceAndWindow(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.InitialWindow = 2; c.MinimumWindow = 1; c.MaxPayload = 100 })
	ready(t, m)
	pp := packets(t, m.Step(Event{Kind: Write, Data: bytes.Repeat([]byte{1}, 500)}, 0))
	if len(pp) != 2 {
		t.Fatal("window exceeded", pp)
	}
	// ACK for unsent packet cannot release its storage.
	m.Step(incoming(DataACK, 4, ""), core.Instant(time.Millisecond))
	if m.Usage().TXBytes != 500 {
		t.Fatal("freed unsent data")
	}
	// Exact ACK releases only sequence 1, and can force packet 0 just once.
	a := m.Step(incoming(DataACK, 1, ""), core.Instant(2*time.Millisecond))
	if m.Usage().TXBytes != 400 {
		t.Fatal(m.Usage())
	}
	for _, p := range packets(t, a) {
		if p.Sequence.Low == 0 && p.Transmissions > 2 {
			t.Fatal("hole re-marked", p)
		}
	}
	if got := windowAt(1000, 80, 10000, 0); got != 995 {
		t.Fatal("initial loss window", got)
	}
	if got := windowAt(1000, 80, 2, 0); got != 3 {
		t.Fatal("queue-limited window", got)
	}
	if got := windowAt(1000, 80, 10000, time.Hour); got != 6500 {
		t.Fatal("ceiling", got)
	}
}
func TestBoundedOwnershipAndSlowReader(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.RXBytes = 4; c.RXPackets = 3; c.TXBytes = 4; c.TXPackets = 2 })
	ready(t, m)
	p := incoming(Data, 0, "abc")
	m.Step(p, 0)
	p.Packet.Payload[0] = 'X'
	a := m.Step(Event{Kind: Read, Count: 1}, 0)
	for _, v := range a {
		if v.Kind == Bytes && string(v.Data) != "a" {
			t.Fatal(v)
		}
	}
	if m.Usage().RXCapacity != 2 {
		t.Fatal("partial read retains large allocation", m.Usage())
	}
	m.Step(incoming(Data, 1, "de"), 0)
	// Duplicate doesn't charge bytes or count.
	m.Step(incoming(Data, 1, "de"), 0)
	if m.Usage().RXBytes != 4 {
		t.Fatal(m.Usage())
	}
	a = m.Step(incoming(Data, 2, "f"), 0)
	if m.Discards()[7] != 1 || m.Status() != Established || m.Usage().RXBytes != 4 {
		t.Fatal("overflow", a, m.Usage())
	}
	m = newMachine(t, nil)
	ready(t, m)
	data := []byte("abc")
	a = m.Step(Event{Kind: Write, Data: data}, 0)
	data[0] = 'X'
	for _, v := range a {
		if v.Kind == Send {
			v.Data[len(v.Data)-1] = 'Z'
		}
	}
	pp := packets(t, m.Step(Event{Kind: Tick}, core.Instant(500*time.Millisecond)))
	if len(pp) != 1 || string(pp[0].Payload) != "abc" {
		t.Fatal("aliased retained TX", pp)
	}
}
func TestTeardownCatalog(t *testing.T) {
	for _, tt := range []struct {
		name   string
		event  Event
		types  []Type
		failed bool
	}{
		{"abort", Event{Kind: LocalAbort}, []Type{Abort, Quit}, true},
		{"peer-abort", incoming(Abort, 0, ""), []Type{AbortACK, Quit}, true},
		{"peer-quit", incoming(Quit, 0, ""), []Type{Quit}, true},
		{"peer-exit", incoming(Exit, 0, ""), []Type{Quit}, true},
		{"need-dtls", incoming(NeedDTLS, 0, ""), []Type{Quit}, true},
		{"transport", Event{Kind: TransportFailed}, []Type{Quit}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t, nil)
			ready(t, m)
			m.Step(Event{Kind: Write, Data: []byte("x")}, 0)
			a := m.Step(tt.event, 0)
			pp := packets(t, a)
			if len(pp) != len(tt.types) {
				t.Fatal(pp)
			}
			for i, p := range pp {
				if p.Type != tt.types[i] {
					t.Fatal(pp)
				}
			}
			if m.Status() != Stopped || has(a, Failed) != tt.failed || !has(a, CancelTimer) || m.Credit() != 0 || m.Usage().TXBytes != 0 {
				t.Fatal(a, m.Usage())
			}
			if len(m.Step(tt.event, 0)) != 0 {
				t.Fatal("repeated termination produced effects")
			}
		})
	}
	m := newMachine(t, nil)
	m.Step(Event{Kind: Start}, 0)
	a := m.Step(Event{Kind: Cancel}, 0)
	pp := packets(t, a)
	if len(pp) != 2 || pp[0].Type != Exit || pp[1].Type != Quit {
		t.Fatal(pp)
	}
}
func TestFINPreservesFinalPartialRead(t *testing.T) {
	m := newMachine(t, nil)
	ready(t, m)
	m.Step(incoming(Data, 0, "abc"), 0)
	e := incoming(FIN, 0, "")
	e.Packet.Sequence = Sequence{0xffffffff, 0xffffffff}
	a := m.Step(e, 0)
	if !has(a, Closed) || has(a, End) {
		t.Fatal(a)
	}
	a = m.Step(Event{Kind: Read, Count: 1}, 0)
	if !has(a, Bytes) || has(a, End) {
		t.Fatal(a)
	}
	a = m.Step(Event{Kind: Read, Count: 9}, 0)
	if !has(a, Bytes) || !has(a, End) || m.Usage().RXBytes != 0 {
		t.Fatal(a)
	}
}
func TestLongRTTSamplePreservesEstimator(t *testing.T) {
	m := newMachine(t, nil)
	m.now = core.Instant(20 * time.Second)
	m.sample(txPacket{sent: true})
	if m.srtt != 20*time.Second || m.rttvar != 10*time.Second || m.RTO() != 8*time.Second {
		t.Fatal(m.srtt, m.rttvar, m.RTO())
	}
}

func TestClockExhaustionStopsInsteadOfRearmingNow(t *testing.T) {
	m := newMachine(t, nil)
	ready(t, m)
	a := m.Step(Event{Kind: Tick}, core.Instant(1<<63-1))
	if m.Status() != Stopped || !has(a, Failed) {
		t.Fatal(a)
	}
	m = newMachine(t, nil)
	a = m.Step(Event{Kind: Start}, core.Instant(1<<63-1))
	if m.Status() != Stopped || has(a, ArmTimer) {
		t.Fatal("timer at exhausted epoch", a)
	}
}
func TestUrgentBypassesWindowAndPreservesOrder(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.InitialWindow = 1; c.MinimumWindow = 1; c.MaxPayload = 100 })
	ready(t, m)
	m.Step(Event{Kind: Write, Data: make([]byte, 200)}, 0)
	pp := packets(t, m.Step(Event{Kind: WriteUrgent, Data: []byte("urgent")}, 0))
	if len(pp) != 1 || pp[0].Type != Urgent || pp[0].Sequence.Low != 2 {
		t.Fatal(pp)
	}
	receiver := newMachine(t, nil)
	ready(t, receiver)
	a := receiver.Step(incoming(Urgent, 1, "b"), 0)
	if has(a, Readable) || packets(t, a)[0].Type != UrgentACK {
		t.Fatal(a)
	}
	receiver.Step(incoming(Data, 0, "a"), 0)
	a = receiver.Step(Event{Kind: Read, Count: 8196}, 0)
	for _, v := range a {
		if v.Kind == Bytes && string(v.Data) != "ab" {
			t.Fatal(v)
		}
	}
}
func TestSACKInclusiveMultipleRangesAndExactACK(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.MaxPayload = 100 })
	ready(t, m)
	m.Step(Event{Kind: Write, Data: make([]byte, 600)}, 0)
	sack, _ := (SACK{Count: 2, Entries: []Range{{Sequence{Low: 1}, Sequence{Low: 2}}, {Sequence{Low: 4}, Sequence{Low: 4}}}}).Encode()
	m.Step(Event{Kind: Receive, Packet: Packet{Type: SACK2, ID: 7, Payload: sack}}, 0)
	if m.Usage().TXBytes != 300 {
		t.Fatal(m.Usage())
	}
	for i, want := range []uint32{0, 3, 5} {
		if m.tx[i].packet.Sequence.Low != want {
			t.Fatal(m.tx)
		}
	}
	m.Step(incoming(UrgentACK, 3, ""), 0)
	if m.Usage().TXBytes != 200 {
		t.Fatal(m.Usage())
	}
	m.Step(incoming(ACK2, 0, ""), 0)
	if m.Usage().TXBytes != 100 || m.tx[0].packet.Sequence.Low != 5 {
		t.Fatal(m.Usage(), m.tx)
	}
}
func TestWatchdogAndLegacyCreation(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.IOTCVersion = 5 })
	m.Step(Event{Kind: Start}, 0)
	a := m.Step(Event{Kind: Receive, Packet: Packet{Type: Heartbeat, Version: 3}}, 0)
	if !has(a, Ready) || m.RemoteID() != 0 {
		t.Fatal(a, m.RemoteID())
	}
	m.Step(Event{Kind: Tick}, core.Instant(119*time.Second))
	if m.Status() != Established {
		t.Fatal("early watchdog")
	}
	a = m.Step(Event{Kind: Tick}, core.Instant(120*time.Second))
	if !has(a, Failed) {
		t.Fatal("late watchdog", a)
	}
}
func TestFINACKAndPrematureFIN(t *testing.T) {
	m := newMachine(t, nil)
	m.Step(Event{Kind: Start}, 0)
	fin := incoming(FIN, 0, "")
	fin.Packet.Sequence = Sequence{0xffffffff, 0xffffffff}
	if pp := packets(t, m.Step(fin, 0)); len(pp) != 0 {
		t.Fatal("ACKed pre-creation FIN", pp)
	}
	m = newMachine(t, nil)
	ready(t, m)
	ack := incoming(FINACK, 0, "")
	ack.Packet.Sequence = Sequence{0xffffffff, 0xffffffff}
	m.Step(ack, 0)
	if m.Status() != Established {
		t.Fatal("unsolicited FIN_ACK closed connection")
	}
	pp := packets(t, m.Step(Event{Kind: Close}, 0))
	if len(pp) != 1 || pp[0].Type != FIN {
		t.Fatal(pp)
	}
	a := m.Step(ack, core.Instant(time.Millisecond))
	if !has(a, Closed) || !has(a, End) || !has(a, CancelTimer) {
		t.Fatal(a)
	}
}
func TestMalformedPacketAndConfiguration(t *testing.T) {
	m := newMachine(t, nil)
	ready(t, m)
	for _, p := range []Packet{{Type: Data, Payload: make([]byte, MaxData+1)}, {Type: SACK2, Payload: make([]byte, 72)}, {Type: FIN, Payload: []byte{0}}} {
		before := m.Usage()
		if a := m.Step(Event{Kind: Receive, Packet: p}, 0); len(a) != 0 || m.Usage() != before {
			t.Fatal(a)
		}
	}
	for _, edit := range []func(*Config){func(c *Config) { c.LocalID = 0 }, func(c *Config) { c.LocalID = 255 }, func(c *Config) { c.MaxPayload = 99 }, func(c *Config) { c.TXPackets = 0 }, func(c *Config) { c.RXBytes = 0 }, func(c *Config) { c.CloseTimeout = 0 }, func(c *Config) { c.CreateTimeout = -1 }, func(c *Config) { c.InitialWindow = 6501 }} {
		c := DefaultConfig()
		edit(&c)
		if _, err := New(c); err == nil {
			t.Fatal("invalid configuration accepted", c)
		}
	}
}
func FuzzMachine(f *testing.F) {
	f.Add([]byte{1, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 255})
	f.Add([]byte{0, 0, 0, 0, 2, 1, 2, 1, 2, 1})
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 4096 {
			b = b[:4096]
		}
		m := newMachine(t, func(c *Config) {
			c.TXBytes = 512
			c.RXBytes = 2000
			c.TXPackets = 8
			c.RXPackets = 8
			c.MaxPayload = 100
		})
		ready(t, m)
		var now core.Instant
		for i, v := range b {
			now = after(now, time.Duration(v)*time.Millisecond)
			e := Event{Kind: Tick}
			switch v % 12 {
			case 0:
				e = Event{Kind: Write, Data: b[:min(len(b), 200)]}
			case 1:
				e = incoming(Data, uint32(v/12), string(b[:min(len(b), 100)]))
			case 2:
				e = incoming(ACK2, uint32(v/12), "")
			case 3:
				e = incoming(DataACK, uint32(v/12), "")
			case 4:
				e = Event{Kind: Read, Count: int(v) + 1}
			case 5:
				e = Event{Kind: Receive, Packet: Packet{Type: Heartbeat, Version: 5, ID: 9}}
			case 6:
				e = Event{Kind: WriteUrgent, Data: []byte{v}}
			case 7:
				if p, err := Decode(b[i:]); err == nil {
					e = Event{Kind: Receive, Packet: p}
				}
			case 8:
				e = Event{Kind: Close}
			case 9:
				e = Event{Kind: LocalAbort}
			}
			a := m.Step(e, now)
			for _, p := range packets(t, a) {
				if len(p.Payload) > MaxData {
					t.Fatal("oversize output")
				}
			}
			u := m.Usage()
			if u.TXBytes > 512 || u.RXBytes > 2000 || u.TXPackets > 9 || u.RXPackets > 8 || u.RXCapacity != u.RXBytes || m.Credit() < 0 {
				t.Fatal(u, m.Credit())
			}
		}
	})
}

func TestPartialReadReannouncesReadability(t *testing.T) {
	m := newMachine(t, nil)
	ready(t, m)
	m.Step(incoming(Data, 0, "abc"), 0)
	a := m.Step(Event{Kind: Read, Count: 1}, 0)
	if !has(a, Readable) || !has(a, Bytes) {
		t.Fatal("remaining bytes need another read opportunity", a)
	}
}

func TestDuplicateACKRepeatsOnFallback(t *testing.T) {
	m := newMachine(t, nil)
	ready(t, m)
	m.Step(incoming(Data, 0, "a"), 0)
	m.Step(Event{Kind: Tick}, core.Instant(100*time.Millisecond))
	a := m.Step(incoming(Data, 0, "a"), core.Instant(101*time.Millisecond))
	if pp := packets(t, a); len(pp) != 1 || pp[0].Type != ACK2 {
		t.Fatal(pp)
	}
	a = m.Step(Event{Kind: Tick}, core.Instant(201*time.Millisecond))
	if pp := packets(t, a); len(pp) != 1 || pp[0].Type != ACK2 || pp[0].Sequence.Low != 0 {
		t.Fatal("duplicate must leave cumulative work pending", pp)
	}
}
func TestQuitDuringCreationIsFailure(t *testing.T) {
	m := newMachine(t, nil)
	m.Step(Event{Kind: Start}, 0)
	a := m.Step(incoming(Quit, 0, ""), 0)
	if !has(a, Failed) || has(a, Closed) {
		t.Fatal(a)
	}
}
func TestAttemptByteWrapNeverEnablesRTTSampling(t *testing.T) {
	m := newMachine(t, nil)
	ready(t, m)
	m.Step(Event{Kind: Write, Data: []byte("x")}, 0)
	m.tx[0].packet.Transmissions = 255
	pp := packets(t, m.Step(Event{Kind: Tick}, core.Instant(500*time.Millisecond)))
	if len(pp) != 1 || pp[0].Transmissions != 0 {
		t.Fatal(pp)
	}
	m.Step(incoming(ACK2, 0, ""), core.Instant(600*time.Millisecond))
	if m.RTO() != time.Second {
		t.Fatal("wrapped counter enabled ambiguous sample", m.RTO())
	}
}
func TestPacketCountBudgetAndTerminalGap(t *testing.T) {
	m := newMachine(t, func(c *Config) { c.RXPackets = 1 })
	ready(t, m)
	m.Step(incoming(Data, 0, "a"), 0)
	if a := m.Step(incoming(Data, 1, "b"), 0); m.Discards()[7] != 1 || m.Status() != Established {
		t.Fatal("packet count overflow", a)
	}
	m = newMachine(t, nil)
	ready(t, m)
	m.Step(incoming(Data, 1, "b"), 0)
	e := incoming(FIN, 0, "")
	e.Packet.Sequence = Sequence{0xffffffff, 0xffffffff}
	if a := m.Step(e, 0); !has(a, Failed) || has(a, End) {
		t.Fatal("gap reported as EOF", a)
	}
}
