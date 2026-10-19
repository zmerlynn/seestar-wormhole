package p2ptunnel

import (
	"bytes"
	"testing"
)

func TestReservedSetupSend(t *testing.T) {
	// Receive-only padding cannot be sent by the raw encoder.
	for _, wire := range []string{
		"010710000200125c7f000001a5a5a5a5a5a5a5a5",
		"010734000200125c7f00000100000000000000007800a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5",
	} {
		f, err := Decode(unhex(t, wire), Established)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Encode(Established); err == nil {
			t.Error("sent nonzero SETUP padding or service tail")
		}
	}
}

func TestReservedSetupReceive(t *testing.T) {
	for _, tc := range []struct {
		wire    string
		legacy  bool
		service string
	}{
		{"010710000200125c7f000001a5a5a5a5a5a5a5a5", true, ""},
		{"010734000200125c7f000001a5a5a5a5a5a5a5a57800a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5", false, "x"},
	} {
		f, err := Decode(unhex(t, tc.wire), Established)
		if err != nil {
			t.Fatal(err)
		}
		s, err := ParseSetup(f.Payload)
		if err != nil || s != (Setup{Port: 4700, Address: [4]byte{127, 0, 0, 1}, Service: tc.service, Legacy: tc.legacy}) {
			t.Fatalf("setup=%+v err=%v", s, err)
		}
		canonical, err := s.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(canonical[8:16], make([]byte, 8)) {
			t.Fatal("sent padding")
		}
		if !tc.legacy && (!bytes.Equal(canonical[18:], make([]byte, 34)) || canonical[17] != 0) {
			t.Fatal("sent service tail")
		}
	}
	// A NUL in byte 49 cannot rescue an unterminated 33-byte service field.
	b := unhex(t, "0200125c7f0000010000000000000000")
	b = append(b, bytes.Repeat([]byte{'x'}, 33)...)
	b = append(b, 0, 0, 0)
	if _, err := ParseSetup(b); err == nil {
		t.Fatal("accepted unterminated SETUP service")
	}
}
