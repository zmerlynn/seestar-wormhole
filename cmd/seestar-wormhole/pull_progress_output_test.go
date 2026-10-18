package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/pull"
)

func TestPullProgressTerminalLine(t *testing.T) {
	var sink progressBuffer
	w := &pullProgressOutput{output: &sink, tty: true}
	w.update(pull.ProgressSnapshot{FilesDone: 2, FilesTotal: 7, BytesDone: 100, BytesTotal: 9000, Rate: 12.5})
	const first = "progress files=2/7 bytes=100/9000 rate=12.5B/s"
	if progressText(w, &sink) != "\r"+first {
		t.Fatalf("first line=%q", &sink)
	}
	w.update(pull.ProgressSnapshot{})
	const shorter = "progress files=0/0 bytes=0/0 rate=0B/s"
	want := "\r" + first + "\r" + shorter + strings.Repeat(" ", len(first)-len(shorter))
	if progressText(w, &sink) != want {
		t.Fatalf("overwrite=%q want=%q", &sink, want)
	}
	const record = "{\"msg\":\"safe log\"}\n"
	if _, err := w.Write([]byte(record)); err != nil {
		t.Fatal(err)
	}
	want += "\r" + strings.Repeat(" ", len(shorter)) + "\r" + record
	if progressText(w, &sink) != want {
		t.Fatalf("log clearing=%q want=%q", &sink, want)
	}
}

func TestPullProgressTTYRateAndStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, p := pull.NewProgress(context.Background())
		var sink progressBuffer
		w := &pullProgressOutput{output: &sink, tty: true}
		stop := w.start(p)
		synctest.Wait()
		one := progressText(w, &sink)
		time.Sleep(999 * time.Millisecond)
		synctest.Wait()
		if progressText(w, &sink) != one {
			t.Fatal("updated before one second")
		}
		time.Sleep(time.Millisecond)
		synctest.Wait()
		if strings.Count(progressText(w, &sink), "progress ") != 2 {
			t.Fatalf("one second=%q", &sink)
		}
		stop()
		stopped := progressText(w, &sink)
		if !strings.HasSuffix(stopped, "\r"+strings.Repeat(" ", len(one)-1)+"\r") {
			t.Fatalf("stop left live line: %q", stopped)
		}
		time.Sleep(time.Hour)
		synctest.Wait()
		if progressText(w, &sink) != stopped {
			t.Fatal("progress survived stop")
		}
	})
}

func TestPullProgressRedirectedBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, p := pull.NewProgress(context.Background())
		var sink progressBuffer
		w := newPullProgressOutput(&sink)
		stop := w.start(p)
		synctest.Wait()
		time.Sleep(29 * time.Second)
		synctest.Wait()
		if strings.Count(progressText(w, &sink), "\n") != 1 {
			t.Fatal("redirected update before 30 seconds")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if strings.Count(progressText(w, &sink), "\n") != 2 {
			t.Fatal("missing 30-second update")
		}
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		stop()
		if n := strings.Count(progressText(w, &sink), "\n"); n != 120 || sink.Len() > 30*1024 || strings.ContainsAny(progressText(w, &sink), "\r\x1b") {
			t.Fatalf("bound: lines=%d bytes=%d", n, sink.Len())
		}
	})
}

func TestPullProgressSerializesLogs(t *testing.T) {
	var sink progressBuffer
	w := &pullProgressOutput{output: &sink}
	var writers sync.WaitGroup
	writers.Go(func() {
		for range 100 {
			_, _ = w.Write([]byte("{\"msg\":\"safe\"}\n"))
		}
	})
	writers.Go(func() {
		for range 100 {
			w.update(pull.ProgressSnapshot{})
		}
	})
	writers.Wait()
	lines := strings.Split(strings.TrimSuffix(progressText(w, &sink), "\n"), "\n")
	if n := strings.Count(sink.String(), "{\"msg\":\"safe\"}\n"); n != 100 {
		t.Fatalf("log records=%d want=100", n)
	}
	for _, line := range lines {
		if line != "{\"msg\":\"safe\"}" && line != "progress files=0/0 bytes=0/0 rate=0B/s" {
			t.Fatalf("interleaved line=%q", line)
		}
	}
}

// stderr supports concurrent writes; use the same contract for in-memory sinks.
type progressBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *progressBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *progressBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
func (b *progressBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Len()
}
func (b *progressBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buffer.Reset()
}
func progressText(_ *pullProgressOutput, sink *progressBuffer) string { return sink.String() }
