package p2ptunnel_test

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"testing"
	"time"
)

type reviewSink struct {
	bytes.Buffer
	written chan struct{}
}

func (s *reviewSink) Write(b []byte) (int, error) {
	n, err := s.Buffer.Write(b)
	s.written <- struct{}{}
	return n, err
}
func (*reviewSink) Close() error { return nil }

// A partially accepted frame is redacted from the exported trace.
// Storage acknowledgement prevents queue loss from concealing leaked opcodes.
func TestPartialSendRedaction(t *testing.T) {
	m, _ := p2ptunnel.New(p2ptunnel.DefaultConfig())
	sink := &reviewSink{written: make(chan struct{}, 1)}
	w := tracerecord.New(sink, tracerecord.SizeCap, func() time.Time { return time.Unix(0, 0) })
	now := core.Instant(0)
	step := func(e p2ptunnel.Event) []p2ptunnel.Action {
		now++
		aa := m.Step(e, now)
		w.Record("tunnel", 1, now, e, aa)
		<-sink.written
		return aa
	}
	complete := func(aa []p2ptunnel.Action) {
		for len(aa) > 0 {
			a := aa[0]
			aa = aa[1:]
			if a.Kind == p2ptunnel.Send {
				aa = append(aa, step(p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: a.Token, Count: len(a.Data)})...)
			}
		}
	}
	complete(step(p2ptunnel.Event{Kind: p2ptunnel.Start}))
	pass, _ := (p2ptunnel.Frame{Command: p2ptunnel.PassCommand, Payload: (p2ptunnel.LoginResult{}).Encode()}).Encode(p2ptunnel.LoginResponse)
	complete(step(p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: pass}))
	var handle p2ptunnel.Action
	aa := step(p2ptunnel.Event{Kind: p2ptunnel.Open, Port: 54321, Request: 1})
	for _, a := range aa {
		if a.Kind == p2ptunnel.Opened {
			handle = a
		}
	}
	complete(aa)
	var read p2ptunnel.Action
	for _, a := range step(p2ptunnel.Event{Kind: p2ptunnel.ReadReady, Channel: handle.Channel, Generation: handle.Generation}) {
		if a.Kind == p2ptunnel.ReadLocal {
			read = a
		}
	}
	secret := "SENSITIVE-password-token-UID"
	aa = step(p2ptunnel.Event{Kind: p2ptunnel.LocalData, Channel: read.Channel, Generation: read.Generation, Token: read.Token, Data: []byte(secret + "padding")})
	var send p2ptunnel.Action
	for _, a := range aa {
		if a.Kind == p2ptunnel.Send {
			send = a
		}
	}
	aa = step(p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: send.Token, Count: p2ptunnel.HeaderSize})
	for len(aa) > 0 {
		var next []p2ptunnel.Action
		for _, a := range aa {
			if a.Kind == p2ptunnel.Send {
				next = append(next, step(p2ptunnel.Event{Kind: p2ptunnel.Sent, Token: a.Token, Count: 1})...)
			}
		}
		aa = next
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if w.Dropped() != 0 {
		t.Fatalf("dropped %d", w.Dropped())
	}
	rr, err := tracerecord.Read(bytes.NewReader(sink.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var recovered []byte
	for _, r := range rr {
		if r.Input.Kind == uint8(p2ptunnel.Sent) {
			for _, a := range r.Outputs {
				if a.Kind == uint8(p2ptunnel.Send) && a.Length >= p2ptunnel.HeaderSize {
					recovered = append(recovered, byte(a.Type))
				}
			}
		}
	}
	if len(recovered) == 0 {
		t.Fatal("continuation sends not exercised")
	}
	if bytes.Contains(recovered, []byte(secret)) {
		t.Fatalf("payload recovered from numeric type fields: %q", recovered)
	}
	for _, r := range rr {
		if r.Input.Kind == uint8(p2ptunnel.Sent) && (r.Input.Count == 1 || r.Input.Count == p2ptunnel.HeaderSize) {
			for _, a := range r.Outputs {
				if a.Kind == uint8(p2ptunnel.Send) && a.Type != uint16(p2ptunnel.DataCommand) {
					t.Fatalf("continuation opcode=%d want DATA", a.Type)
				}
			}
		}
	}
}
