package p2ptunnel

import (
	"bytes"
	"encoding/binary"
	"github.com/go-json-experiment/json"
	"os"
	"reflect"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"github.com/zmerlynn/seestar-wormhole/internal/wiretest"
)

type metamorphicFixture struct {
	Name, Wire, RDT, DTLS string
	VariantWire           string `json:"variant_wire"`
	VariantRDT            string `json:"variant_rdt"`
	VariantDTLS           string `json:"variant_dtls"`
	Phase                 Phase
	Role                  Role
	Tolerated             []string `json:"tolerated,omitempty"`
}

func metamorphicFixtures(t testing.TB) []metamorphicFixture {
	t.Helper()
	b, err := os.ReadFile("testdata/metamorphic.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []metamorphicFixture
	if err = json.Unmarshal(b, &rows, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	return rows
}
func metamorphicDriver(t *testing.T, row metamorphicFixture, observers ...func(Event, []Action)) *driver {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Role = row.Role
	cfg.AllowedPorts = []uint16{4700}
	d := newDriver(t, cfg)
	if len(observers) > 0 {
		d.observeTurn = observers[0]
	}
	d.step(Event{Kind: Start})
	if row.Phase != Established {
		return d
	}
	login := "080008000000000000000000"
	if row.Role == Server {
		login = "070008000008030400000000"
	}
	if row.Name == "port-response" {
		login = "080008000000000001000000"
	}
	d.step(Event{Kind: Stream, Data: unhex(t, login)})
	if row.Name == "port-response" {
		d.step(Event{Kind: Open, Port: 4700, Request: 23})
		if d.m.pending == nil || d.m.pending.id != 1 {
			t.Fatal("fixture must correlate ID 1")
		}
	} else if row.Role == Agent {
		a := d.open(4700)
		// Channel 7 is independently fixed by each peer fixture. Public allocation
		// starts at 1, so open seven local sockets, preserving active siblings.
		for a.Channel < 7 {
			a = d.open(4700)
		}
		if a.Channel != 7 {
			t.Fatal("fixture channel not allocated")
		}
	}
	return d
}

// Retain every action field. Reserved receive bytes never become a local effect
// or sender byte.
func metamorphicActions(t *testing.T, in []Action) []Action {
	t.Helper()
	var out []Action
	for _, a := range in {
		out = append(out, a)
	}
	return out
}
func metamorphicMeaning(t *testing.T, row metamorphicFixture, wire []byte) any {
	t.Helper()
	f, err := Decode(wire, row.Phase)
	if err != nil {
		t.Fatal(err)
	}
	if row.Phase == LoginRequest {
		l, err := ParseLogin(f.Payload)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	if row.Phase == LoginResponse {
		r, err := ParseLoginResult(f.Payload)
		if err != nil {
			t.Fatal(err)
		}
		return struct {
			Command       Command
			Port, Service bool
			Known         bool
		}{f.Command, r.PortVerify != 0, r.ServiceVerify != 0, len(f.Payload) >= 8}
	}
	switch f.Command {
	case SetupCommand:
		s, err := ParseSetup(f.Payload)
		if err != nil {
			t.Fatal(err)
		}
		return s
	case CloseCommand:
		if len(f.Payload) == 0 {
			return CloseOutcome{}
		}
		return CloseOutcome{HasResult: true, Result: f.Payload[0]}
	case PortRequest, PortResponse:
		p, err := ParsePort(f.Payload)
		if err != nil {
			t.Fatal(err)
		}
		p.Reserved = 0
		if f.Command == PortRequest {
			p.Valid = 0
		} else if p.Valid != 0 {
			p.Valid = 1
		}
		return p
	case ServiceRequest, ServiceResponse:
		s, err := ParseService(f.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if f.Command == ServiceRequest {
			s.Valid = 0
		} else if s.Valid != 0 {
			s.Valid = 1
		}
		return s
	default:
		return f
	}
}

// PASS handling normalizes flags, ignores the signed result, and reads flags
// only at L>=8. SETUP and CLOSE permit opaque suffixes.
func metamorphicMutate(t *testing.T, row metamorphicFixture, noise []byte) []byte {
	t.Helper()
	b := unhex(t, row.Wire)
	fill := byte(0xa5)
	if len(noise) > 0 {
		fill = noise[0]
	}
	fill |= 1
	if row.Phase == LoginRequest {
		b[1] = fill
		return b
	}
	if row.Phase == LoginResponse {
		b[1] = fill
		if b[0] == byte(PassCommand) && len(b) >= 12 {
			for _, i := range []int{8, 10} {
				if b[i] != 0 {
					b[i] = fill
				}
			}
			b[9] = fill
			b[11] = fill
			clear(b[4:8])
			b[7] = 0x80 // signed result is not inspected for PASS.
			b = append(b, noise...)
		}
	} else {
		switch Command(b[0]) {
		case SetupCommand:
			b[4] = fill
			b[5] = fill
			for i := 12; i < 20; i++ {
				b[i] = fill
			}
			if len(b) >= 56 {
				// Retain the first NUL; ignore the rest of
				// its field, trailing three bytes and any extended suffix.
				nul := bytes.IndexByte(b[20:53], 0) + 20
				for i := nul + 1; i < 56; i++ {
					b[i] = fill
				}
				b = append(b, noise...)
			}
		case CloseCommand:
			if len(b) > 4 {
				b = append(b, noise...)
			}
		case PortRequest, PortResponse:
			b[1] = fill
			b[9] = fill
			if Command(b[0]) == PortRequest || b[8] != 0 {
				b[8] = fill
			}
		case ServiceRequest, ServiceResponse:
			b[1] = fill
			// Request validity is unused; responses use
			// nonzero truth. Service IDs end at their first NUL.
			nul := bytes.IndexByte(b[6:39], 0) + 6
			for i := nul + 1; i < 39; i++ {
				b[i] = fill
			}
			if Command(b[0]) == ServiceRequest || b[39] != 0 {
				b[39] = fill
			}
		}
	}
	binary.LittleEndian.PutUint16(b[2:], uint16(len(b)-4))
	return b
}
func metamorphicRun(t *testing.T, row metamorphicFixture, wire, unknown []byte, split int, observers ...func(Event, []Action)) (*driver, []Action) {
	t.Helper()
	d := metamorphicDriver(t, row, observers...)
	// Re-authenticate each mutated tunnel payload as DTLS-protected RDT DATA.
	// RDT transport consumes its envelope; tunnel sees the exact ordered payload.
	protect := func(stream []byte, seq uint64) []byte {
		packet := unhex(t, "5a97c2f102050000000000000000000001070000")
		binary.LittleEndian.PutUint16(packet[6:], uint16(len(stream)))
		packet = append(packet, stream...)
		protected := wiretest.Seal(t, packet, seq)
		plain, err := wiretest.Open(protected, wiretest.Key())
		if err != nil {
			t.Fatal(err)
		}
		// This test exercises tunnel parsing, not an independent RDT implementation.
		if !bytes.Equal(plain, packet) {
			t.Fatal("protected RDT payload differs")
		}
		p, err := rdt.Decode(plain)
		if err != nil {
			t.Fatal(err)
		}
		return p.Payload
	}
	stream := append(bytes.Clone(unknown), wire...)
	stream = protect(stream, 2)
	split %= len(stream) + 1
	var actions []Action
	for _, part := range [][]byte{stream[:split], stream[split:]} {
		if len(part) > 0 {
			actions = append(actions, metamorphicActions(t, d.step(Event{Kind: Stream, Data: part}))...)
		}
	}
	return d, actions
}
func checkMetamorphicReceive(t *testing.T, row metamorphicFixture, noise []byte, split int) {
	t.Helper()
	base := unhex(t, row.Wire)
	changed := metamorphicMutate(t, row, noise)
	plain, err := wiretest.Open(unhex(t, row.DTLS), wiretest.Key())
	if err != nil || !bytes.Equal(plain, unhex(t, row.RDT)) || !bytes.Equal(plain[20:], base) {
		t.Fatal("retained authenticated fixture differs", err)
	}
	if !reflect.DeepEqual(metamorphicMeaning(t, row, base), metamorphicMeaning(t, row, changed)) {
		t.Fatal("semantic projection differs")
	}
	var unknown []byte
	if row.Phase == Established {
		unknown = append(unhex(t, "fe630000"), noise...)
		binary.LittleEndian.PutUint16(unknown[2:], uint16(len(noise)))
	}
	d0, a0 := metamorphicRun(t, row, base, nil, 0)
	d1, a1 := metamorphicRun(t, row, changed, unknown, split)
	// A partial read can re-arm the same timer before it completes. Check those
	// idempotent scheduling effects separately, preserving all other action fields.
	stripTimers := func(in []Action) []Action {
		var out []Action
		for _, a := range in {
			if a.Kind == ArmTimer {
				if a.At != d0.m.timer && a.At != d1.m.timer {
					t.Fatal("unexpected timer deadline")
				}
				continue
			}
			out = append(out, a)
		}
		return out
	}
	// Decoder storage contains already-consumed opaque bytes; Buffered must be
	// zero. The last returned action slice is checked above, not retained state.
	m0, m1 := *d0.m, *d1.m
	if m0.decoder.Buffered() != 0 || m1.decoder.Buffered() != 0 || m0.decoder.want != 0 || m1.decoder.want != 0 || m0.decoder.failed || m1.decoder.failed {
		t.Fatal("complete stream retained or poisoned parser state")
	}
	m0.decoder, m1.decoder = Decoder{}, Decoder{}
	m0.actions, m1.actions = nil, nil
	if !reflect.DeepEqual(stripTimers(a0), stripTimers(a1)) || !reflect.DeepEqual(m0, m1) {
		t.Fatalf("%s changed actions/state: %+v / %+v", row.Name, a0, a1)
	}
	// Datagram extent must remain exact even for an extensible body.
	for _, b := range [][]byte{changed[:len(changed)-1], append(bytes.Clone(changed), 0)} {
		if _, err := Decode(b, row.Phase); err == nil {
			t.Fatal("inexact extent accepted")
		}
	}
	protected := wiretest.Seal(t, changed, 3)
	wrong := bytes.Clone(wiretest.Key())
	wrong[0] ^= 1
	if _, err := wiretest.Open(protected, wrong); err == nil {
		t.Fatal("wrong key accepted")
	}
	protected[len(protected)-1] ^= 1
	if _, err := wiretest.Open(protected, wiretest.Key()); err == nil {
		t.Fatal("bad tag accepted")
	}
}
func TestMetamorphicReceive(t *testing.T) {
	for _, row := range metamorphicFixtures(t) {
		t.Run(row.Name, func(t *testing.T) {
			for _, n := range []int{0, 1, 15, 64, 256} {
				noise := bytes.Repeat([]byte{0xa5}, n)
				changed := metamorphicMutate(t, row, noise)
				for _, split := range []int{0, 1, 3, 4, len(changed) / 2, len(changed)} {
					checkMetamorphicReceive(t, row, noise, split)
				}
			}
			variant := metamorphicMutate(t, row, bytes.Repeat([]byte{0xa5}, 15))
			if !bytes.Equal(variant, unhex(t, row.VariantWire)) {
				t.Fatal("retained mutation differs")
			}
			authenticated, err := wiretest.Open(unhex(t, row.VariantDTLS), wiretest.Key())
			if err != nil || !bytes.Equal(authenticated, unhex(t, row.VariantRDT)) {
				t.Fatal("retained mutation authentication differs", err)
			}
			packet, err := rdt.Decode(authenticated)
			if err != nil || !bytes.Equal(packet.Payload, variant) {
				t.Fatal("retained mutation RDT differs", err)
			}
			b := unhex(t, row.Wire)
			for n := 0; n < len(b); n++ {
				if _, err := Decode(b[:n], row.Phase); err == nil {
					t.Fatalf("truncation %d accepted", n)
				}
			}
		})
	}
}
func FuzzMetamorphicReceive(f *testing.F) {
	rows := metamorphicFixtures(f)
	for i := range rows {
		f.Add(uint8(i), uint16(3), []byte{0xa5})
	}
	f.Fuzz(func(t *testing.T, which uint8, split uint16, noise []byte) {
		if len(noise) > 256 {
			return
		}
		checkMetamorphicReceive(t, rows[int(which)%len(rows)], noise, int(split))
	})
}
