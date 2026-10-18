package main

import (
	"bytes"
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/tracerecord"
	"io"
	"os"
	"testing"
)

func TestTraceFlagPrivateAndDefaultOff(t *testing.T) {
	o, err := parse(append([]string{"proxy"}, nil...), io.Discard)
	if err != nil || o.trace != "" {
		t.Fatalf("default trace %q %v", o.trace, err)
	}
	path := t.TempDir() + "/trace.jsonl"
	var logs bytes.Buffer
	err = run(context.Background(), append([]string{"proxy"}, []string{"--trace", path, "--bind", "invalid address"}...), io.Discard, &logs)
	if err == nil {
		t.Fatal("expected listener failure")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rr, err := tracerecord.Read(bytes.NewReader(b))
	if err != nil || len(rr) != 1 || rr[0].Record != "end" {
		t.Fatalf("unclosed trace %s %v", b, err)
	}
	if bytes.Contains(logs.Bytes(), []byte(path)) {
		t.Fatal("path leaked")
	}
	if err = run(context.Background(), append([]string{"proxy"}, []string{"--trace", path}...), io.Discard, &logs); err == nil {
		t.Fatal("existing trace accepted")
	}
}
