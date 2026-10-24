//go:build unix

package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// Actual SIGINT/SIGTERM while a lease is live, with an unreachable stop service.
// The shorter configured bound makes this a brief subprocess test; the five-
// second production default is exercised by the unmodified virtual-clock probe.
func TestActualSignalsWithOwedRelease(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOwedSignalHelper$", "-test.v")
			cmd.Env = append(os.Environ(), "REVIEW2_OWED_SIGNAL=1")
			pipe, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			ready, stops := false, 0
			scanner := bufio.NewScanner(pipe)
			for scanner.Scan() {
				line := scanner.Text()
				if line == "REVIEW2_READY" {
					ready = true
					if err := cmd.Process.Signal(sig); err != nil {
						t.Fatal(err)
					}
				}
				if line == "REVIEW2_STOP" {
					stops++
				}
				t.Log(line)
			}
			if err := scanner.Err(); err != nil {
				t.Error(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("owed release helper=%v", err)
			}
			if !ready || stops != 1 {
				t.Errorf("ready=%t stop markers=%d, want true/1", ready, stops)
			}
		})
	}
}

type owedSignalFactory struct {
	h             *signalHandle
	allocs, stops int
	elapsed       time.Duration
	deadline      time.Time
	finished      time.Time
	cause         error
}

func (f *owedSignalFactory) New(uint64, session.Budgets) (sessionedge.Layers, error) {
	return sessionedge.Layers{Cloud: f, Rendezvous: f.h, DTLS: f.h, RDT: f.h, Tunnel: f.h}, nil
}
func (f *owedSignalFactory) Allocate(context.Context) (sessionedge.Lease, error) {
	f.allocs++
	return f, nil
}
func (f *owedSignalFactory) Stop(ctx context.Context) error {
	f.stops++
	f.deadline, _ = ctx.Deadline()
	fmt.Println("REVIEW2_STOP")
	start := time.Now()
	<-ctx.Done()
	f.finished = time.Now()
	f.elapsed = f.finished.Sub(start)
	f.cause = ctx.Err()
	return errors.Join(session.ErrReleaseAbandoned, f.cause)
}

type owedSignalSink struct{}

func (*owedSignalSink) Try(session.Action) error { return nil }
func (*owedSignalSink) Observe(_ session.Event, before, after session.Snapshot, _ core.Instant) {
	if before.State != session.Ready && after.State == session.Ready {
		fmt.Println("REVIEW2_READY")
	}
}
func TestOwedSignalHelper(t *testing.T) {
	if os.Getenv("REVIEW2_OWED_SIGNAL") != "1" {
		t.Skip("subprocess helper")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	cfg := session.DefaultConfig()
	cfg.StopTimeout = 100 * time.Millisecond
	f := &owedSignalFactory{h: &signalHandle{notices: make(chan session.Event, 1)}}
	err := sessionedge.Run(ctx, cfg, zeroRetryRNG{}, f, nil, &owedSignalSink{})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, session.ErrStop) {
		t.Errorf("final signal outcome=%v, want cancellation+ErrStop", err)
	}
	if f.allocs != 1 || f.stops != 1 {
		t.Errorf("allocs/stops=%d/%d, want 1/1", f.allocs, f.stops)
	}
	if f.deadline.IsZero() || f.finished.Before(f.deadline) || !errors.Is(f.cause, context.DeadlineExceeded) {
		t.Errorf("stop deadline/finish/cause=%v/%v/%v, want expired actual deadline", f.deadline, f.finished, f.cause)
	}
	t.Logf("one owed release elapsed=%v after signal, allocations=%d stops=%d", f.elapsed, f.allocs, f.stops)
}
