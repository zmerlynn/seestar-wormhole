package edge

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"testing"
	"time"
)

type traceSink struct{ bytes.Buffer }

func (*traceSink) Close() error { return nil }

func TestDTLSAdapterTraceBoundaries(t *testing.T) {
	sink := new(traceSink)
	rec := tracerecord.New(sink, 4096, time.Now)
	p, err := NewDTLSPackets(1388)
	if err != nil {
		t.Fatal(err)
	}
	p.SetTrace(rec, 1)
	p.allowApplication()
	// Literal RFC 6347 application header with synthetic secret-bearing content.
	secret := []byte("synthetic-PSK-password-token")
	b := append([]byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 0, 0, byte(len(secret))}, secret...)
	if err = p.Deliver(1, b); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1388)
	if _, _, err = p.ReadFrom(buf); err != nil {
		t.Fatal(err)
	}
	if _, err = p.WriteTo(b, nil); err != nil {
		t.Fatal(err)
	}
	<-p.Outgoing()
	p.EnableWriteCompletion()
	p.CompleteWrite(nil)
	p.SetDeadline(time.Time{})
	p.Close()
	rec.Close()
	rr, err := tracerecord.Read(bytes.NewReader(sink.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uint8]bool{}
	for _, r := range rr {
		if r.Record == "step" {
			seen[r.Input.Kind] = true
			if r.Machine != "dtls" || r.Instance != 1 || len(r.Outputs) != 1 {
				t.Fatalf("bad adapter record %+v", r)
			}
		}
	}
	for _, kind := range []uint8{0, 4, 6, 8, 10, 12, 14} {
		if !seen[kind] {
			t.Fatalf("missing boundary %d", kind)
		}
	}
	if bytes.Contains(sink.Bytes(), secret) {
		t.Fatal("adapter bytes leaked")
	}
}
