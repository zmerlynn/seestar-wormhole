package proxy

import (
	"context"
	"encoding/hex"
	"testing"
	"testing/synctest"
	"time"
)

func TestC14AuthenticatedUnknownThenRPC(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	literal, err := hex.DecodeString("5a97c2f144050000000000000000000000070000")
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s, _, cloud := workloadSession(t, ctx, func(p *simulatedWire) { p.rdtPrefix = literal }, Config{})
		defer s.Close()
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatalf("unknown terminated composed session: %v", err)
		}
		defer c.Close()
		if err := exchange(c.(*stream), 101, 0); err != nil {
			t.Fatalf("valid RPC after unknown: %v", err)
		}
		c.Close()
		s.Close()
		if cloud.allocs.Load() != 1 || cloud.stops.Load() != 1 {
			t.Fatalf("unexpected redial: allocations=%d stops=%d", cloud.allocs.Load(), cloud.stops.Load())
		}

	})
}
