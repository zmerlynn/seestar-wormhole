package p2ptunnel

import (
	"bytes"
	"testing"
)

func TestDecodedFrameOwnsPayload(t *testing.T) {
	input := unhex(t, "02010300616263")
	f, err := Decode(input, Established)
	if err != nil {
		t.Fatal(err)
	}
	input[4] = 'x'
	if !bytes.Equal(f.Payload, []byte("abc")) {
		t.Fatalf("input mutation changed owned payload: %q", f.Payload)
	}
	var d Decoder
	_, first, err := d.Feed(unhex(t, "02010300616263"), Established)
	if err != nil || first == nil {
		t.Fatalf("first=%v err=%v", first, err)
	}
	_, second, err := d.Feed(unhex(t, "0202030078797a"), Established)
	if err != nil || second == nil {
		t.Fatalf("second=%v err=%v", second, err)
	}
	if !bytes.Equal(first.Payload, []byte("abc")) || !bytes.Equal(second.Payload, []byte("xyz")) {
		t.Fatalf("reused decoder storage: first=%q second=%q", first.Payload, second.Payload)
	}
}

func TestStaleWriteTokenOnActiveGeneration(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	ch := d.open(4700)
	first := mustAction(t, d.frame(Frame{DataCommand, ch.Channel, []byte("abc")}), WriteLocal)
	d.frame(Frame{DataCommand, ch.Channel, []byte("def")})
	second := mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: 3}, first)), WriteLocal)
	if first.Generation != second.Generation || first.Token == second.Token {
		t.Fatal("setup must retain generation and replace token")
	}
	before, usage := d.m.Channel(ch.Channel), d.m.Usage()
	got := d.step(local(Event{Kind: LocalWritten, Count: 3}, first))
	if len(got) != 0 || d.m.Channel(ch.Channel) != before || d.m.Usage() != usage {
		t.Fatalf("stale token progressed active write: actions=%+v state=%+v usage=%+v", got, d.m.Channel(ch.Channel), d.m.Usage())
	}
	d.step(local(Event{Kind: LocalWritten, Count: 3}, second))
	if d.m.Channel(ch.Channel).Queued != 0 {
		t.Fatal("matching second token failed to drain")
	}
}
