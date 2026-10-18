package keylog

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestWriteFailureDoesNotReachProducer(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "keys")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	w := New(f, nil)
	defer w.Close()
	if n, err := w.Write([]byte(nss)); n != len(nss) || err != nil {
		t.Fatalf("diagnostic failure reached producer: n=%d err=%v", n, err)
	}
}

type recoveringSink struct {
	writes int
	lines  []byte
}

func (s *recoveringSink) Write(p []byte) (int, error) {
	s.writes++
	if s.writes == 1 {
		return 0, os.ErrPermission
	}
	s.lines = append(s.lines, p...)
	return len(p), nil
}
func (*recoveringSink) Close() error { return nil }

func TestWriterRecoversAndReportsOnce(t *testing.T) {
	var logs lossLogs
	sink := new(recoveringSink)
	w := New(sink, slog.New(slog.NewJSONHandler(&logs, nil)))
	for range 3 {
		w.Write([]byte(nss))
	}
	w.Close()
	if w.Dropped() != 1 || !bytes.Equal(sink.lines, bytes.Repeat([]byte(nss), 2)) {
		t.Fatal("failure became sticky")
	}
	if strings.Count(logs.String(), "keylog_failed") != 1 || !strings.Contains(logs.String(), `"count":1`) || strings.Contains(logs.String(), "permission") {
		t.Fatal(logs.String())
	}
}

type blockedSink struct {
	entered chan struct{}
	release chan struct{}
}

func (s blockedSink) Write(p []byte) (int, error) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return len(p), nil
}
func (blockedSink) Close() error { return nil }

func TestQueueFullAndShutdownBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs lossLogs
		sink := blockedSink{make(chan struct{}, 1), make(chan struct{})}
		w := New(sink, slog.New(slog.NewJSONHandler(&logs, nil)))
		defer func() { close(sink.release); <-w.done }()
		w.Write([]byte(nss))
		<-sink.entered
		for range queueSize + 3 {
			w.Write([]byte(nss))
		}
		if w.Dropped() != 3 {
			t.Fatalf("drops=%d want 3", w.Dropped())
		}
		synctest.Wait()
		if strings.Count(logs.String(), "keylog_failed") != 1 {
			t.Fatal("missing overload warning")
		}
		start := time.Now()
		w.Close()
		if elapsed := time.Since(start); elapsed != ShutdownBound {
			t.Fatalf("shutdown=%v", elapsed)
		}
	})
}

func TestNilLoggerDoesNotUseProcessDefault(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	sink := new(recoveringSink)
	w := New(sink, nil)
	w.Write([]byte(nss))
	w.Close()
	if w.Dropped() != 1 {
		t.Fatal("missing storage failure")
	}
	if logs.Len() != 0 {
		t.Fatal("keylog used process-default logger without an owned logger")
	}
}
