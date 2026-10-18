//go:build windows

package main

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"
)

type heldLog struct {
	entered, release chan struct{}
	once             sync.Once
	bytes.Buffer
}

func (s *heldLog) Write(b []byte) (int, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.Buffer.Write(b)
}
func (*heldLog) Close() error { return nil }

func TestWindowsBlockedLogDoesNotBlockShutdown(t *testing.T) {
	sink := &heldLog{entered: make(chan struct{}), release: make(chan struct{})}
	w := newQueuedLog(sink)
	w.Write([]byte("first\n"))
	<-sink.entered
	for range 2 * logQueueSize {
		if _, err := w.Write([]byte("line\n")); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	w.close()
	if time.Since(start) > time.Second {
		t.Fatal("blocked log stalled close")
	}
	close(sink.release)
	<-w.done
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
