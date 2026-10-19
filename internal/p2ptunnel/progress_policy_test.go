package p2ptunnel

import (
	"bytes"
	"crypto/sha256"
	"testing"
	"time"
)

func TestCapacityProgressGrace(t *testing.T) {
	for _, tc := range []struct {
		name       string
		age        time.Duration
		completion string
		wantStuck  bool
	}{
		{"before-grace", time.Second - time.Nanosecond, "none", false},
		{"at-grace", time.Second, "none", true},
		{"partial-progress", time.Second, "partial", false},
		{"zero-is-not-progress", time.Second, "zero", true},
		{"writable-is-not-progress", time.Second, "writable", true},
		{"stale-token-is-not-progress", time.Second, "token", true},
		{"stale-generation-is-not-progress", time.Second, "generation", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.ChannelBytes, cfg.SessionBytes, cfg.HighWater = 4*MaxData, 4*MaxData, 1024
			d := activeDriver(t, cfg, false)
			healthy, stuck, rpc := d.open(445), d.open(445), d.open(4700)
			input := bytes.Repeat([]byte{0x71}, MaxData)
			w := mustAction(t, d.frame(Frame{DataCommand, healthy.Channel, input}), WriteLocal)
			for i := 0; i < 2; i++ {
				d.frame(Frame{DataCommand, healthy.Channel, input})
			}
			sw := mustAction(t, d.frame(Frame{DataCommand, stuck.Channel, input}), WriteLocal)
			d.now = d.now.Add(tc.age)
			d.step(local(Event{Kind: LocalWritten, Count: 1}, w))
			d.frame(Frame{DataCommand, healthy.Channel, []byte{0x71}})
			e := local(Event{Kind: LocalWritten, Count: 1}, sw)
			switch tc.completion {
			case "partial":
				d.step(e)
				d.frame(Frame{DataCommand, stuck.Channel, []byte{0x71}})
			case "zero":
				e.Count = 0
				d.step(e)
			case "writable":
				e.Kind = LocalWritable
				d.step(e)
			case "token":
				e.Token++
				d.step(e)
			case "generation":
				e.Generation++
				d.step(e)
			}
			out := d.frame(Frame{DataCommand, rpc.Channel, []byte("rpc\n")})
			want := healthy.Channel // Both within grace: the holder furthest above equal share.
			if tc.wantStuck {
				want = stuck.Channel
			}
			if got := mustAction(t, out, CloseLocal).Channel; got != want {
				t.Fatalf("victim=%d want=%d", got, want)
			}
			if got := mustAction(t, out, WriteLocal); got.Channel != rpc.Channel || string(got.Data) != "rpc\n" {
				t.Fatalf("RPC delivery=%+v", got)
			}
		})
	}
}

func TestCapacityNewBacklogGetsGrace(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ChannelBytes, cfg.SessionBytes, cfg.HighWater = 4*MaxData, 4*MaxData, 1024
	d := activeDriver(t, cfg, false)
	a, b, rpc := d.open(445), d.open(445), d.open(4700)
	input := bytes.Repeat([]byte{0x71}, MaxData)
	w := mustAction(t, d.frame(Frame{DataCommand, b.Channel, input}), WriteLocal)
	d.step(local(Event{Kind: LocalWritten, Count: MaxData}, w))
	d.now = d.now.Add(10 * time.Second)
	w = mustAction(t, d.frame(Frame{DataCommand, a.Channel, input}), WriteLocal)
	for i := 0; i < 2; i++ {
		d.frame(Frame{DataCommand, a.Channel, input})
	}
	d.frame(Frame{DataCommand, b.Channel, input})
	d.now = d.now.Add(time.Second / 2)
	d.step(local(Event{Kind: LocalWritten, Count: 1}, w))
	d.frame(Frame{DataCommand, a.Channel, []byte{0x71}})
	if got := mustAction(t, d.frame(Frame{DataCommand, rpc.Channel, []byte("rpc\n")}), CloseLocal).Channel; got != a.Channel {
		t.Fatalf("new backlog was aged by idle time: victim=%d want=%d", got, a.Channel)
	}
}

func TestCapacityIncomingDataDoesNotRenewProgress(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ChannelBytes, cfg.SessionBytes, cfg.HighWater = 4*MaxData, 4*MaxData, 1024
	d := activeDriver(t, cfg, false)
	healthy, stuck, rpc := d.open(445), d.open(445), d.open(4700)
	input := bytes.Repeat([]byte{0x71}, MaxData)
	w := mustAction(t, d.frame(Frame{DataCommand, healthy.Channel, input}), WriteLocal)
	for i := 0; i < 2; i++ {
		d.frame(Frame{DataCommand, healthy.Channel, input})
	}
	d.frame(Frame{DataCommand, stuck.Channel, input[:MaxData-1]})
	d.now = d.now.Add(time.Second)
	d.step(local(Event{Kind: LocalWritten, Count: 1}, w))
	d.frame(Frame{DataCommand, healthy.Channel, []byte{0x71}})
	// Receiving bytes is peer progress, not successful local delivery. Keep
	// this stalled holder aged even when its delayed tail arrives now.
	d.frame(Frame{DataCommand, stuck.Channel, []byte{0x71}})
	out := d.frame(Frame{DataCommand, rpc.Channel, []byte("rpc\n")})
	if got := mustAction(t, out, CloseLocal).Channel; got != stuck.Channel {
		t.Fatalf("incoming DATA renewed stall grace: victim=%d want=%d", got, stuck.Channel)
	}
}

func TestCapacityReclaimsSeveralStalledBeforeProgressing(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ChannelBytes, cfg.SessionBytes, cfg.HighWater = 4*MaxData, 4*MaxData, 1024
	d := activeDriver(t, cfg, false)
	healthy, a, b, rpc := d.open(445), d.open(445), d.open(445), d.open(4700)
	input := bytes.Repeat([]byte{0x71}, MaxData)
	w := mustAction(t, d.frame(Frame{DataCommand, healthy.Channel, input}), WriteLocal)
	for i := 0; i < 2; i++ {
		d.frame(Frame{DataCommand, healthy.Channel, input})
	}
	for _, ch := range []Action{a, b} {
		d.frame(Frame{DataCommand, ch.Channel, input[:MaxData/2]})
	}
	d.now = d.now.Add(time.Second)
	d.step(local(Event{Kind: LocalWritten, Count: 1}, w))
	d.frame(Frame{DataCommand, healthy.Channel, []byte{0x71}})
	out := d.frame(Frame{DataCommand, rpc.Channel, input})
	var victims []byte
	for _, a := range out {
		if a.Kind == CloseLocal {
			victims = append(victims, a.Channel)
		}
	}
	if !bytes.Equal(victims, []byte{a.Channel, b.Channel}) || !d.m.Channel(healthy.Channel).Active {
		t.Fatalf("victims=%v want=[%d %d], progressing active=%v", victims, a.Channel, b.Channel, d.m.Channel(healthy.Channel).Active)
	}
}

func TestCapacityProgressingFullContentUnderPressure(t *testing.T) {
	d := activeDriver(t, legacyCapacityConfig(), false)
	healthy, stuck, rpc := d.open(445), d.open(445), d.open(4700)
	input := bytes.Repeat([]byte{0x71}, MaxData)
	var w Action
	for _, burst := range []struct {
		ch   Action
		size int
	}{{healthy, 5 << 20}, {stuck, 3 << 20}} {
		for i := 0; i < burst.size/MaxData; i++ {
			out := d.frame(Frame{DataCommand, burst.ch.Channel, input})
			if i == 0 && burst.ch.Channel == healthy.Channel {
				w = mustAction(t, out, WriteLocal)
			}
		}
	}
	sink := sha256.New()
	received, replies := 0, 0
	complete := func() []Action {
		sink.Write(w.Data)
		received += len(w.Data)
		d.now = d.now.Add(time.Duration(len(w.Data)) * time.Second / (64 << 10))
		return d.step(local(Event{Kind: LocalWritten, Count: len(w.Data)}, w))
	}
	for i := 0; i < 8; i++ {
		w = mustAction(t, complete(), WriteLocal)
	}
	for i := 0; i < 8; i++ {
		d.frame(Frame{DataCommand, healthy.Channel, input})
	}
	poll := func() {
		out := d.frame(Frame{DataCommand, rpc.Channel, []byte("rpc\n")})
		for _, a := range out {
			if a.Kind == CloseLocal && a.Channel != stuck.Channel {
				t.Fatalf("progressing victim=%d", a.Channel)
			}
		}
		wr := mustAction(t, out, WriteLocal)
		if wr.Channel != rpc.Channel || string(wr.Data) != "rpc\n" {
			t.Fatal("RPC corrupt")
		}
		d.step(local(Event{Kind: LocalWritten, Count: len(wr.Data)}, wr))
		replies++
	}
	poll()
	for d.m.Channel(healthy.Channel).Queued > 0 {
		out := complete()
		if d.m.Channel(healthy.Channel).Queued > 0 {
			w = mustAction(t, out, WriteLocal)
		}
		if received%(64<<10) == 0 {
			poll()
		}
	}
	const volume = (5 << 20) + (64 << 10)
	want := sha256.Sum256(bytes.Repeat([]byte{0x71}, volume))
	if received != volume || !bytes.Equal(sink.Sum(nil), want[:]) || !d.m.Channel(healthy.Channel).Active || d.m.Channel(stuck.Channel).Active || replies < 80 || d.m.Status() != Active {
		t.Fatalf("received=%d hash=%x want=%d/%x RPC=%d healthy=%v stuck=%v status=%v", received, sink.Sum(nil), volume, want, replies, d.m.Channel(healthy.Channel).Active, d.m.Channel(stuck.Channel).Active, d.m.Status())
	}
	t.Logf("received=%d SHA256=%x RPC=%d elapsed=%s", received, sink.Sum(nil), replies, time.Duration(d.now))
}
