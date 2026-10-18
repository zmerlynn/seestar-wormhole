package edge

import (
	"bytes"
	"errors"
	profile "github.com/zmerlynn/seestar-wormhole/internal/dtls"
	"testing"
)

func TestDTLSEarlyApplicationAdmission(t *testing.T) {
	// Literal RFC 6347 envelopes, deliberately independent of Pion's encoder.
	app := []byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 2, 0, 1, 42}
	ccs := []byte{20, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 1}
	finished := []byte{22, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 99}
	alert := []byte{21, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 3, 0, 2, 1, 0}
	for _, records := range [][][]byte{
		{app}, {app, app}, {ccs, app, finished}, {app, ccs, finished, app}, {ccs, finished, app, alert},
	} {
		p := packetPath(t, 1388)
		var datagram, want []byte
		for _, r := range records {
			datagram = append(datagram, r...)
			if !bytes.Equal(r, app) {
				want = append(want, r...)
			}
		}
		original := bytes.Clone(datagram)
		if err := p.Deliver(1, datagram); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(datagram, original) {
			t.Fatal("Deliver mutated caller buffer")
		}
		if len(want) == 0 {
			select {
			case got := <-p.rx:
				t.Fatalf("early application queued: %x", got)
			default:
			}
		} else {
			select {
			case got := <-p.rx:
				if !bytes.Equal(got, want) {
					t.Fatalf("control records = %x, want %x", got, want)
				}
			default:
				t.Fatal("coalesced control records discarded")
			}
		}
		if p.controlBytes != len(want) {
			t.Fatalf("control bytes = %d, want %d", p.controlBytes, len(want))
		}
	}
	p := packetPath(t, 1388)
	for i := 0; i < 1024; i++ {
		if err := p.Deliver(1, app); err != nil {
			t.Fatalf("early record %d exhausted storage: %v", i, err)
		}
	}
	if err := p.Deliver(1, finished); err != nil {
		t.Fatalf("early records blocked Finished: %v", err)
	}
}

func TestDTLSPionAdmissionOwnershipAndAccounting(t *testing.T) {
	app := []byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 2, 0, 1, 42}
	ccs := []byte{20, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 1}
	unknown := []byte{99, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 3, 0, 1, 99}
	p := packetPath(t, 1388)
	want := bytes.Join([][]byte{ccs, unknown}, nil)
	for i := 0; i < 512; i++ {
		wire := bytes.Join([][]byte{app, ccs, app, unknown, app}, nil)
		if err := p.Deliver(1, wire); err != nil {
			t.Fatalf("datagram %d: %v", i, err)
		}
		clear(wire) // Ownership must survive reuse after filtering.
		if got := <-p.rx; !bytes.Equal(got, want) {
			t.Fatalf("queued control = %x, want %x", got, want)
		}
	}
	if p.controlBytes != 512*len(want) || p.controlCount != 512 {
		t.Fatalf("control accounting = %d bytes/%d datagrams, want %d/512", p.controlBytes, p.controlCount, 512*len(want))
	}
	if err := p.Deliver(1, ccs); !errors.Is(err, ErrDTLSBudget) {
		t.Fatalf("513th control datagram: %v, want budget error", err)
	}
	if err := p.Deliver(1, app); err != nil || len(p.rx) != 0 {
		t.Fatalf("early application at control ceiling: %v, queued %d", err, len(p.rx))
	}
}

func TestDTLSPionRejectedDatagramIsAtomic(t *testing.T) {
	ccs := []byte{20, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 1}
	headerOnly := []byte{22, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	plaintext := []byte{23, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 2, 0, 1, 42}
	for _, ready := range []bool{false, true} {
		p := packetPath(t, 1388)
		if ready {
			p.allowApplication()
		}
		for _, wire := range [][]byte{headerOnly, bytes.Join([][]byte{ccs, headerOnly}, nil), bytes.Join([][]byte{ccs, plaintext}, nil), bytes.Repeat(ccs, 100)} {
			if err := p.Deliver(1, wire); err == nil {
				t.Fatalf("ready=%t accepted invalid datagram of size %d", ready, len(wire))
			}
			if len(p.rx) != 0 || p.controlBytes != 0 || p.controlCount != 0 {
				t.Fatalf("rejected datagram queued or accounted a prefix: ready=%t", ready)
			}
		}
	}
}

// Exercise Pion decoding through admission as well as the envelope validator. Rejected
// packets must not enqueue a valid prefix; accepted early data consumes no slot.
func FuzzDTLSAdmission(f *testing.F) {
	f.Add([]byte{22, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 42})
	f.Add([]byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 42,
		22, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 1, 0, 0})
	// Pion accepts a header-only prefix when another record follows. Filtering
	// that application record leaves a header-only control datagram: compare
	// the original decode's slices rather than decoding the filtered wire again.
	f.Add([]byte{22, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 42})
	f.Fuzz(func(t *testing.T, data []byte) {
		p := packetPath(t, 1388)
		original := bytes.Clone(data)
		control, n, decodeErr := profile.DecodeDatagram(data)
		err := p.Deliver(1, data)
		if !bytes.Equal(original, data) {
			t.Fatal("caller data mutated")
		}
		select {
		case got := <-p.rx:
			if err != nil {
				t.Fatal("rejected datagram queued a prefix")
			}
			if decodeErr != nil || n != len(got) || !bytes.Equal(got, bytes.Join(control, nil)) {
				t.Fatalf("queued control = %x, want decoded records %x, error %v", got, control, decodeErr)
			}
		default:
			if decodeErr == nil && n != 0 {
				t.Fatal("valid control records dropped")
			}
		}
	})
}
