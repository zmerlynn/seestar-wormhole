package rdt

import (
	"bytes"
	"reflect"
	"testing"
)

// Independent transcriptions of the RDT envelope and complete message catalog.
// Synthetic fields deliberately exercise both sequence words and reserved bytes.
func TestLiteralCatalogMeaning(t *testing.T) {
	const sack = "000000000100000003000000010000000800000001000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
	for _, tt := range []struct {
		name, wire, payload string
		kind                Type
	}{
		{"heartbeat", "5a97c2f101050000090000000200000002073412", "", Heartbeat},
		{"data", "5a97c2f102050300090000000200000002073412616263", "616263", Data},
		{"fin", "5a97c2f103050000090000000200000002073412", "", FIN},
		{"quit", "5a97c2f104050000090000000200000002073412", "", Quit},
		{"urgent", "5a97c2f110050300090000000200000002073412616263", "616263", Urgent},
		{"abort", "5a97c2f120050000090000000200000002073412", "", Abort},
		{"heartbeat-ack", "5a97c2f141050000090000000200000002073412", "", HeartbeatACK},
		{"data-ack", "5a97c2f142050000090000000200000002073412", "", DataACK},
		{"fin-ack", "5a97c2f143050000090000000200000002073412", "", FINACK},
		{"sack2", "5a97c2f145054800090000000200000002073412" + sack, sack, SACK2},
		{"ack2", "5a97c2f146050000090000000200000002073412", "", ACK2},
		{"urgent-ack", "5a97c2f150050000090000000200000002073412", "", UrgentACK},
		{"abort-ack", "5a97c2f160050000090000000200000002073412", "", AbortACK},
		{"exit", "5a97c2f170050000090000000200000002073412", "", Exit},
		{"need-dtls", "5a97c2f171050000090000000200000002073412", "", NeedDTLS},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wire := unhex(t, tt.wire)
			want := Packet{Type: tt.kind, Version: 5, Sequence: Sequence{9, 2}, Transmissions: 2, ID: 7, Reserved: 0x1234, Payload: unhex(t, tt.payload)}
			got, err := Decode(wire)
			if err != nil || got.Type != want.Type || got.Version != want.Version || got.Sequence != want.Sequence || got.Transmissions != want.Transmissions || got.ID != want.ID || got.Reserved != want.Reserved || !bytes.Equal(got.Payload, want.Payload) {
				t.Fatalf("decode=%+v, %v; want %+v", got, err, want)
			}
			// Encode the independent expected value, never the decoded value.
			out, err := want.Encode()
			if err != nil || !bytes.Equal(out, wire) {
				t.Fatalf("encode=%x, %v; want %x", out, err, wire)
			}
		})
	}
	want := SACK{Count: 1, Entries: []Range{{Sequence{3, 1}, Sequence{8, 1}}, {}, {}, {}}}
	got, err := DecodeSACK(unhex(t, sack))
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("sack=%+v, %v; want %+v", got, err, want)
	}
	out, err := want.Encode()
	if err != nil || !bytes.Equal(out, unhex(t, sack)) {
		t.Fatalf("sack encode=%x, %v", out, err)
	}
}

func TestOrdinarySequenceAdvance(t *testing.T) {
	for _, tt := range []struct{ from, to Sequence }{
		{Sequence{0, 0}, Sequence{1, 0}}, {Sequence{123, 7}, Sequence{124, 7}},
		{Sequence{0xefffffff, 4}, Sequence{0xf0000000, 4}},
	} {
		if got := tt.from.Next(); got != tt.to {
			t.Fatalf("%+v.Next()=%+v; want %+v", tt.from, got, tt.to)
		}
	}
}
