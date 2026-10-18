package edge

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord/tracetest"
	"testing"
	"time"
)

type adapterCapture struct {
	capture *tracetest.Recorder
	now     core.Instant
}

func (c *adapterCapture) RecordDTLS(id uint64, kind uint8, inputLength, outputLength int, datatype uint16, outcome string) {
	c.now++
	c.capture.Step("dtls", id, tracerecord.EdgeEvent{Kind: kind, Length: inputLength, Type: datatype}, []tracerecord.EdgeEvent{{Kind: kind + 1, Length: outputLength, Outcome: outcome}}, c.now)
}

func TestRecordedDTLSAdapterReplay(t *testing.T) {
	capture := tracetest.New(t)
	observer := &adapterCapture{capture: capture}
	p, err := NewDTLSPackets(1388)
	if err != nil {
		t.Fatal(err)
	}
	p.EnableWriteCompletion()
	p.SetTrace(observer, 1)
	// Reuse the literal RFC 6347 admission/boundary scenario with a synthetic
	// application record. The recorder retains only its total length/type.
	wire := []byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 42}
	p.allowApplication()
	if err = p.Deliver(1, wire); err != nil {
		t.Fatal(err)
	}
	if _, _, err = p.ReadFrom(make([]byte, 1388)); err != nil {
		t.Fatal(err)
	}
	p.CompleteWrite(nil)
	if _, err = p.WriteTo(wire, nil); err != nil {
		t.Fatal(err)
	}
	<-p.Outgoing()
	p.SetDeadline(time.Time{})
	p.Close()
	rr := capture.Fixture("testdata/trace-adapter.jsonl")
	fresh, err := NewDTLSPackets(1388)
	if err != nil {
		t.Fatal(err)
	}
	fresh.EnableWriteCompletion()
	defer fresh.Close()
	hydrate := func(r tracerecord.Record) (tracerecord.EdgeEvent, error) {
		data := make([]byte, r.Input.Length)
		if r.Input.Kind == 0 || r.Input.Kind == 4 {
			data = append(data[:0], wire...)
		}
		return tracerecord.EdgeEvent{Kind: r.Input.Kind, Type: r.Input.Type, Data: data}, nil
	}
	step := func(e tracerecord.EdgeEvent, _ core.Instant) []tracerecord.EdgeEvent {
		out := tracerecord.EdgeEvent{Kind: e.Kind + 1}
		switch e.Kind {
		case 0:
			out.Err = fresh.Deliver(byte(e.Type), e.Data)
		case 4:
			n, err := fresh.WriteTo(e.Data, nil)
			out.Err = err
			out.Data = e.Data[:n]
			if err == nil {
				<-fresh.Outgoing()
			}
		case 6:
			n, _, err := fresh.ReadFrom(e.Data)
			out.Err = err
			out.Data = e.Data[:n]
		case 8:
			fresh.CompleteWrite(nil)
		case 10:
			fresh.allowApplication()
		case 12:
			out.Err = fresh.Close()
		case 14:
			out.Err = fresh.SetDeadline(time.Time{})
		default:
			t.Fatalf("unknown adapter input %d", e.Kind)
		}
		return []tracerecord.EdgeEvent{out}
	}
	if err := tracerecord.Replay(rr, "dtls", 1, hydrate, step); err != nil {
		t.Fatal(err)
	}
}
