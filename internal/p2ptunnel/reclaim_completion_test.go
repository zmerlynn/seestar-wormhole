package p2ptunnel

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"reflect"
	"testing"
	"time"
)

func TestReclaimIgnoresVictimWriteCompletions(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ChannelBytes, cfg.SessionBytes, cfg.HighWater = 8192, 8192, 1024
	d := activeDriver(t, cfg, false)
	a, b, rpc := d.open(445), d.open(445), d.open(4700)
	inject := func(id byte, header []byte, size int) []Action {
		t.Helper()
		wire := append(bytes.Clone(header), bytes.Repeat([]byte{0x9a}, size)...)
		wire[1] = id
		return d.step(Event{Kind: Stream, Data: wire})
	}
	first := mustAction(t, inject(a.Channel, []byte{2, 0, 0, 16}, 4096), WriteLocal)
	staleA := mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: 1024}, first)), WriteLocal)
	staleB := mustAction(t, inject(b.Channel, []byte{2, 0, 0, 16}, 4096), WriteLocal)
	if got := d.m.Usage().RXBytes; got != 7168 {
		t.Fatalf("before reclaim bytes=%d want 7168", got)
	}
	d.now = core.Instant(2 * time.Second)
	out := inject(rpc.Channel, []byte{2, 0, 0, 32}, 8192)
	var closed []byte
	for _, action := range out {
		if action.Kind == CloseLocal {
			closed = append(closed, action.Channel)
		}
	}
	if !reflect.DeepEqual(closed, []byte{b.Channel, a.Channel}) {
		t.Fatalf("victims=%v want B then A", closed)
	}
	write := mustAction(t, out, WriteLocal)
	if write.Channel != rpc.Channel || len(write.Data) != 8192 {
		t.Fatalf("recipient write=%+v", write)
	}
	if d.m.Channel(a.Channel).Active || d.m.Channel(b.Channel).Active {
		t.Fatal("reclaimed victims still active")
	}
	// Victim writes were already handed to local workers. Their late completions
	// cannot consume storage now owned by the recipient.
	d.step(local(Event{Kind: LocalWritten, Count: 3072}, staleA))
	d.step(local(Event{Kind: LocalWritten, Count: 4096}, staleB))
	if c := d.m.Channel(rpc.Channel); !c.Active || c.Queued != 8192 || d.m.Usage().RXBytes != 8192 {
		t.Fatalf("victim completions changed recipient: channel=%+v usage=%+v", c, d.m.Usage())
	}
	write = mustAction(t, d.step(local(Event{Kind: LocalWritten, Count: 123}, write)), WriteLocal)
	if len(write.Data) != 8069 || d.m.Usage().RXBytes != 8069 {
		t.Fatalf("partial recipient write: remaining=%d usage=%+v", len(write.Data), d.m.Usage())
	}
	refused := mustAction(t, d.step(local(Event{Kind: LocalWriteFailed}, write)), CloseLocal)
	if refused.Rule != "R069" || refused.DiscardedBytes != 8069 || d.m.Channel(rpc.Channel).Active || d.m.Usage().RXBytes != 0 {
		t.Fatalf("recipient failure: close=%+v usage=%+v", refused, d.m.Usage())
	}
}
