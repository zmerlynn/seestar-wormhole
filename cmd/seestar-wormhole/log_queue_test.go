package main

import (
	"bytes"
	"sync"
	"testing"
	"testing/synctest"
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

func TestQueuedLogCloseDrainBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &heldLog{entered: make(chan struct{}), release: make(chan struct{})}
		w := newQueuedLog(sink)
		defer func() { close(sink.release); <-w.done }()
		w.Write([]byte("held\n"))
		<-sink.entered
		start := time.Now()
		w.close()
		if elapsed := time.Since(start); elapsed != logDrainBound {
			t.Fatalf("blocked log close waited %v, want %v", elapsed, logDrainBound)
		}
		select {
		case <-w.done:
			t.Fatal("log worker exited before sink release")
		default:
		}
	})
}
