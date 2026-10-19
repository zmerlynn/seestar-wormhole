package p2ptunnel

import (
	"bytes"
	"testing"
)

func TestReservedVerificationSend(t *testing.T) {
	for _, valid := range []byte{0, 1, 2, 0x80, 0xff} {
		want := byte(0)
		if valid != 0 {
			want = 1
		}
		b := (PortCheck{ID: 0x1234, Port: 4700, Valid: valid, Reserved: 0xa5}).Encode()
		expected := unhex(t, "34125c120000")
		expected[4] = want
		if !bytes.Equal(b, expected) {
			t.Errorf("port valid=%d: send=%x want=%x", valid, b, expected)
		}
		s, err := (ServiceCheck{ID: 1, Service: "x", Valid: valid}).Encode()
		expected = unhex(t, "010078000000000000000000000000000000000000000000000000000000000000000000")
		expected[35] = want
		if err != nil || !bytes.Equal(s, expected) {
			t.Errorf("service valid=%d: send=%x err=%v want=%x", valid, s, err, expected)
		}
	}
	for _, wire := range []string{"0c00060001005c12ffa5", "0d00060001005c1202a5", "0e00240001007800a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5ff", "0f002400010078000000000000000000000000000000000000000000000000000000000000000002"} {
		f, err := Decode(unhex(t, wire), Established)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Encode(Established); err == nil {
			t.Errorf("sent noncanonical verification %s", wire)
		}
	}
}

func TestReservedVerificationReceive(t *testing.T) {
	for _, tc := range []struct {
		wire           string
		opened, failed bool
	}{
		{"0d00060001005c1201a5", true, false},
		{"0d00060001005c1202ff", true, false},
		{"0d00060001005c12ff01", true, false},
		{"0d00060001005c1200ff", false, true},
		{"0d00060002005c12ff01", false, false},
		{"0d00060001005d12ff01", false, true},
	} {
		d := activeDriver(t, DefaultConfig(), true)
		d.step(Event{Kind: Open, Port: 4700, Request: 23})
		a := d.step(Event{Kind: Stream, Data: unhex(t, tc.wire)})
		_, opened := find(a, Opened)
		_, failed := find(a, OpenFailed)
		if opened != tc.opened || failed != tc.failed {
			t.Fatalf("%s: opened=%v failed=%v", tc.wire, opened, failed)
		}
	}
	for _, tc := range []struct{ wire, service string }{
		{"0e00240001007800a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5ff", "x"},
		{"0f0024000100787900a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5ff", "xy"},
	} {
		f, err := Decode(unhex(t, tc.wire), Established)
		if err != nil {
			t.Fatal(err)
		}
		s, err := ParseService(f.Payload)
		if err != nil || s.Service != tc.service {
			t.Fatalf("service=%+v err=%v", s, err)
		}
	}
	// The optional service-mapping operation is absent. Even a prefix-shaped
	// 0f reply cannot satisfy a pending port check or allocate a channel.
	for _, name := range []string{"78", "7879"} {
		d := activeDriver(t, DefaultConfig(), true)
		d.step(Event{Kind: Open, Port: 4700, Request: 23})
		b := unhex(t, "0f0024000100000000000000000000000000000000000000000000000000000000000000000000ff")
		copy(b[6:], unhex(t, name))
		a := d.step(Event{Kind: Stream, Data: b})
		if _, ok := find(a, Opened); ok || d.m.pending == nil || d.m.Usage().Channels != 0 {
			t.Fatal("service response authorized a port")
		}
	}
	for _, op := range []byte{0x0e, 0x0f} {
		b := unhex(t, "0e0024000100")
		b[0] = op
		b = append(b, bytes.Repeat([]byte{'x'}, 33)...)
		b = append(b, 0)
		if _, err := Decode(b, Established); err == nil {
			t.Fatal("accepted unterminated service")
		}
	}
}
