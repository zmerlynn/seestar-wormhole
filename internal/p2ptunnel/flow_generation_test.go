package p2ptunnel

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

func TestFlowReleasedGenerationCompletion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 2
	d := activeDriver(t, cfg, false)
	a := d.open(445)
	d.auto = false
	pause := mustAction(t, d.frame(Frame{DataCommand, a.Channel, []byte{1, 2, 3}}), Send)
	tail := mustAction(t, d.step(Event{Kind: Sent, Token: pause.Token, Count: 1}), Send)
	d.step(local(Event{Kind: LocalClose}, a))
	d.now = core.Instant(29 * time.Second)
	for range 255 {
		d.open(445)
	}
	b := d.open(445)
	if b.Channel != a.Channel || b.Generation == a.Generation {
		t.Fatalf("reuse old=%+v new=%+v", a, b)
	}
	d.step(Event{Kind: Sent, Token: tail.Token, Count: 3})
	if c := d.m.Channel(b.Channel); !c.Active || c.SentPause || d.m.channels[b.Channel].handedPause {
		t.Fatalf("old frame paused reused channel: %+v", c)
	}
}
