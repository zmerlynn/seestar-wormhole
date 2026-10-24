//go:build windows

package main

import (
	"io"
	"testing"
	"time"
)

func TestWindowsBlockedLogDoesNotBlockShutdown(t *testing.T) {
	sink := &heldLog{entered: make(chan struct{}), release: make(chan struct{})}
	w := newQueuedLog(sink)
	w.Write([]byte("first\n"))
	select {
	case <-sink.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("log worker did not enter sink write")
	}
	for range 2 * logQueueSize {
		w.Write([]byte("line\n"))
	}
	closed := make(chan struct{})
	go func() { w.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(30 * time.Second):
		t.Fatal("blocked log stalled close; deadlock watchdog expired")
	}
	select {
	case <-w.done:
		t.Fatal("log worker exited before sink release")
	default:
	}
	close(sink.release)
	select {
	case <-w.done:
	case <-time.After(30 * time.Second):
		t.Fatal("log worker did not finish after sink release")
	}
	if sink.Len() > 6+(logQueueSize*5) {
		t.Fatal("queue budget exceeded")
	}
	if _, err := w.Write([]byte("late")); err == nil {
		t.Fatal("accepted write after close")
	}
	if _, err := w.Write(make([]byte, maxLogLine+1)); err != io.ErrShortWrite {
		t.Fatal("oversized line accepted")
	}
}
