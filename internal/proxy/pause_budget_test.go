package proxy

import (
	"errors"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestFactoryRequiresEnlargedTunnelReservation(t *testing.T) {
	for _, limit := range []session.Limit{{Bytes: 24 << 20, Count: 2048}, {Bytes: 24 << 20, Count: 12288}, {Bytes: (80 << 20) - 1, Count: 12288}, {Bytes: 80 << 20, Count: 2048}, {Bytes: 80 << 20, Count: 12287}, {Bytes: 80 << 20, Count: 12288}} {
		b := session.DefaultConfig().Budgets
		b.Layers[session.RDT] = session.Limit{Bytes: 16 << 20, Count: 16384}
		b.Layers[session.Tunnel] = limit
		_, err := (&factory{}).New(1, b)
		if limit.Bytes < 80<<20 || limit.Count < 12288 {
			if !errors.Is(err, session.ErrBudget) {
				t.Fatalf("insufficient reservation %+v accepted: %v", limit, err)
			}
		} else if err != nil {
			t.Fatalf("enlarged reservation rejected: %v", err)
		}
	}
}
