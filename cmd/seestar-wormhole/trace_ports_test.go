package main

import (
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"os"
	"testing"
)

func TestTraceUsesOnlyDeviceSideOfMapping(t *testing.T) {
	path := t.TempDir() + "/trace.jsonl"
	w, err := openTrace(path, mappings{{1445, 445}, {4700, 4700}, {14801, 4801}})
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range []uint16{445, 4700, 4801, 1445, 14801, 54321} {
		w.Record("tunnel", 1, 0, p2ptunnel.Event{Kind: p2ptunnel.Open, Port: port}, []p2ptunnel.Action{})
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rr, err := tracerecord.Read(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(rr) != 7 {
		t.Fatalf("records=%d want 7", len(rr))
	}
	for i, r := range rr[:6] {
		if i < 3 {
			if r.Input.Port == 0 || r.Input.PortAlias != "" {
				t.Fatalf("mapped device port: %+v", r.Input)
			}
		} else {
			if r.Input.Port != 0 || r.Input.PortAlias == "" {
				t.Fatalf("private/unmapped port: %+v", r.Input)
			}
		}
	}
}
