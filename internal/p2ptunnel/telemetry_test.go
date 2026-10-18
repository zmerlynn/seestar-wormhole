package p2ptunnel

import (
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

func TestTelemetryViewIsReadOnly(t *testing.T) {
	d := activeDriver(t, DefaultConfig(), false)
	a := d.open(4700)
	d.now = core.Instant(time.Second)
	d.frame(Frame{Command: DataCommand, Channel: a.Channel, Payload: []byte("private")})
	before := d.m.Usage()
	for range 3 {
		v := d.m.QueueStats(a.Channel, core.Instant(2*time.Second))
		if v.RXBytes != 7 || v.BacklogAge != time.Second || v.DeliveryBytes != 7 {
			t.Fatalf("queue view=%+v", v)
		}
	}
	if d.m.Usage() != before {
		t.Fatal("accessor mutated queue")
	}
}
