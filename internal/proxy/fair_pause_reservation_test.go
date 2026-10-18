package proxy

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"testing"
)

func TestFactoryByteReservationIndependent(t *testing.T) {
	for _, n := range []int{24 << 20, (80 << 20) - 1, 80 << 20} {
		b := session.DefaultConfig().Budgets
		b.Layers[session.RDT] = session.Limit{Bytes: 16 << 20, Count: 16384}
		b.Layers[session.Tunnel] = session.Limit{Bytes: n, Count: 12288}
		_, err := (&factory{}).New(1, b)
		if n < 80<<20 && !errors.Is(err, session.ErrBudget) {
			t.Fatalf("under-reserved tunnel bytes=%d accepted with adequate object count: %v", n, err)
		}
		if n == 80<<20 && err != nil {
			t.Fatal(err)
		}
	}
}
