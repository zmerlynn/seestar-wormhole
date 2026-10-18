package main

import (
	"io"
	"strconv"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
)

func TestPauseThresholdFlag(t *testing.T) {
	o, err := parse(append([]string{"proxy"}, nil...), io.Discard)
	if err != nil || o.pauseThreshold != p2ptunnel.DefaultConfig().HighWater {
		t.Fatalf("default threshold=%d err=%v", o.pauseThreshold, err)
	}
	for _, b := range []int{2, 512000, 1 << 20, 2 << 20, 4 << 20, 8 << 20} {
		o, err := parse(append([]string{"proxy"}, []string{"--pause-threshold", strconv.Itoa(b)}...), io.Discard)
		if err != nil || o.pauseThreshold != b {
			t.Fatalf("threshold=%d options=%+v err=%v", b, o, err)
		}
	}
	for _, s := range []string{"-1", "0", "1", "8388609", "bytes"} {
		if _, err := parse(append([]string{"proxy"}, []string{"--pause-threshold", s}...), io.Discard); err == nil {
			t.Fatalf("accepted unsafe threshold %s", s)
		}
	}
}
