package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rclone/rclone/fs/accounting"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
)

// A single selected progress Write stalls; independent log writes still work.
// Release and join even the deliberately abandoned writer before leaving a test.
type stalledProgressSink struct {
	mu               sync.Mutex
	text             strings.Builder
	blockOn, writes  int
	entered, release chan struct{}
}

func (s *stalledProgressSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.writes++
	block := s.writes == s.blockOn
	s.mu.Unlock()
	if block {
		close(s.entered)
		<-s.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text.Write(p)
}

func newStalledProgressSink(n int) *stalledProgressSink {
	return &stalledProgressSink{blockOn: n, entered: make(chan struct{}), release: make(chan struct{})}
}

// Converted from the reviewer's start/stop probe, extended to terminal clear.
func TestPullProgressStalledOutput(t *testing.T) {
	for _, phase := range []string{"initial", "periodic", "clear"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, p := pull.NewProgress(context.Background())
				blockOn := 2
				if phase == "initial" {
					blockOn = 1
				}
				sink := newStalledProgressSink(blockOn)
				w := newPullProgressOutput(sink)
				w.tty = phase == "clear"
				started := make(chan func(), 1)
				go func() { started <- w.start(p) }()
				synctest.Wait()
				var stop func()
				select {
				case stop = <-started:
				default:
					t.Error("start waited for the first progress Write")
					close(sink.release)
					stop = <-started
					stop()
					return
				}
				if phase == "periodic" {
					<-time.After(30 * time.Second)
					<-sink.entered
				}
				stopped := make(chan struct{})
				go func() { stop(); close(stopped) }()
				<-sink.entered
				synctest.Wait()
				select {
				case <-stopped:
					t.Error("shutdown skipped its grace period")
				default:
				}
				<-time.After(999 * time.Millisecond)
				synctest.Wait()
				select {
				case <-stopped:
					t.Error("shutdown grace shorter than one second")
				default:
				}
				<-time.After(time.Millisecond)
				synctest.Wait()
				select {
				case <-stopped:
				default:
					t.Error("stalled progress prevented bounded shutdown")
				}
				close(sink.release)
				<-stopped
				synctest.Wait()
			})
		})
	}
}

func TestPullProgressStalledLogging(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, p := pull.NewProgress(context.Background())
		sink := newStalledProgressSink(2)
		w := newPullProgressOutput(sink)
		w.tty = true
		stop := w.start(p)
		<-time.After(time.Second)
		<-sink.entered
		logged := make(chan struct{})
		go func() { _, _ = w.Write([]byte("safe log\n")); close(logged) }()
		synctest.Wait()
		select {
		case <-logged:
		default:
			t.Error("log waited for an in-flight progress Write")
		}
		close(sink.release)
		<-logged
		stop()
		synctest.Wait()
		if !strings.Contains(sink.text.String(), " \rsafe log\n") {
			t.Errorf("log not cleared in one write: %q", sink.text.String())
		}
	})
}

func TestPullProgressGraceClock(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "join", true: "expire"}[expire], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, p := pull.NewProgress(context.Background())
				sink := newStalledProgressSink(2)
				w := newPullProgressOutput(sink)
				w.tty = true
				armed := make(chan time.Duration, 1)
				clock := make(chan time.Time)
				canceled := false
				w.newGrace = func(d time.Duration) (<-chan time.Time, func()) {
					armed <- d
					return clock, func() { canceled = true }
				}
				stop := w.start(p)
				synctest.Wait()
				done := make(chan struct{})
				go func() { stop(); close(done) }()
				<-sink.entered // final clearing Write
				if d := <-armed; d != time.Second {
					t.Errorf("grace=%v want=1s", d)
				}
				synctest.Wait()
				select {
				case <-done:
					t.Error("writer was not joined through clearing")
				default:
				}
				if expire {
					clock <- time.Now()
				} else {
					close(sink.release)
				}
				<-done
				if !canceled {
					t.Error("grace timer was not stopped")
				}
				if expire {
					close(sink.release)
				}
				synctest.Wait()
				if !strings.HasSuffix(sink.text.String(), " \r") {
					t.Error("missing final clear")
				}
			})
		})
	}
}

func TestPullProgressLatestSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, p := pull.NewProgress(context.Background())
		sink := newStalledProgressSink(1)
		w := newPullProgressOutput(sink)
		stop := w.start(p)
		<-sink.entered
		accounting.Stats(ctx).Bytes(7)
		<-time.After(30 * time.Second)
		synctest.Wait()
		accounting.Stats(ctx).Bytes(2)
		<-time.After(30 * time.Second)
		synctest.Wait()
		close(sink.release)
		synctest.Wait()
		stop()
		if text := sink.text.String(); !strings.Contains(text, "bytes=9/") || strings.Contains(text, "bytes=7/") {
			t.Errorf("expected only latest queued snapshot after stalled initial write: %q", text)
		}
	})
}
