package p2ptunnel

import (
	"bytes"
	"testing"
)

func TestMaximumThresholdBurst(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 8 << 20
	d := activeDriver(t, cfg, false)
	bulk, rpc := d.open(445), d.open(4700)
	volume := cfg.HighWater + 1 + (5 << 20) + MaxData
	b := bytes.Repeat([]byte{0xba}, MaxData)
	for left := volume; left > 0; {
		n := min(left, MaxData)
		d.frame(Frame{DataCommand, bulk.Channel, b[:n]})
		left -= n
	}
	if s := d.m.Channel(bulk.Channel); !s.Active || s.Queued != volume || !s.SentPause {
		t.Fatalf("max threshold burst=%+v want queued %d", s, volume)
	}
	w := mustAction(t, d.frame(Frame{DataCommand, rpc.Channel, []byte("rpc")}), WriteLocal)
	if w.Channel != rpc.Channel || !bytes.Equal(w.Data, []byte("rpc")) {
		t.Fatal("RPC lost")
	}
	t.Logf("B=8MiB retained=%d with no reclaim; usage=%+v", volume, d.m.Usage())
}
func TestBoundedFlowChurnAllSlots(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HighWater = 100
	cfg.TXFrames = 8
	d := activeDriver(t, cfg, false)
	for i := 0; i < 256; i++ {
		c := d.open(445)
		d.frame(Frame{DataCommand, c.Channel, []byte{1}})
	}
	d.auto = false
	pause := mustAction(t, d.step(Event{Kind: ReceiveQueueSize, Count: 256 * 100}), Send)
	peak := 0
	for i := 0; i < 1094; i++ {
		d.step(Event{Kind: ReceiveQueueSize, Count: 0})
		d.step(Event{Kind: ReceiveQueueSize, Count: 256 * 100})
		u := d.m.Usage()
		peak = max(peak, u.ControlFrames)
		if u.ControlFrames > 257 || u.ControlBytes > 1028 || u.TXFrames != 0 || d.m.Status() != Active {
			t.Fatalf("churn usage=%+v status=%v", u, d.m.Status())
		}
	}
	d.step(Event{Kind: ReceiveQueueSize, Count: 0})
	d.step(Event{Kind: Sent, Token: pause.Token, Count: 0})
	if a, ok := find(d.step(Event{Kind: TransportWritable}), Send); ok {
		t.Fatalf("obsolete all-slot flow=%x", a.Data)
	}
	t.Logf("256 retained holders, 1094 reversals, peak control frames=%d bytes=%d data frames=0; final=%+v", peak, peak*4, d.m.Usage())
}
