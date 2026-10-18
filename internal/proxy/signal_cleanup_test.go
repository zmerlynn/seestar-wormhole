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

// Actual OS signal delivery is isolated in a subprocess; all device/cloud
// adapters here are synthetic. The production Run and NotifyContext are used.
func TestActualSignals(t *testing.T) {
	for _, mode := range []string{"transport", "busy", "first-busy"} {
		for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
			t.Run(mode+"/"+sig.String(), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestActualSignalHelper$", "-test.v")
				cmd.Env = append(os.Environ(), "REVIEW_SIGNAL_HELPER="+mode)
				out, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				cmd.Stderr = os.Stderr
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				scanner := bufio.NewScanner(out)
				ready := false
				for scanner.Scan() {
					if scanner.Text() == "REVIEW_BACKOFF" {
						ready = true
						if err = cmd.Process.Signal(sig); err != nil {
							t.Fatal(err)
						}
					}
					t.Log(scanner.Text())
				}
				if err = cmd.Wait(); err != nil {
					t.Fatalf("helper: %v", err)
				}
				if !ready {
					t.Fatal("helper never reached joined release/backoff")
				}
			})
		}
	}
}

type signalFactory struct {
	calls, stops int
	h            *signalHandle
	busy         bool
	firstBusy    bool
}

func (f *signalFactory) New(uint64, session.Budgets) (sessionedge.Layers, error) {
	f.calls++
	return sessionedge.Layers{Cloud: f, Rendezvous: f.h, DTLS: f.h, RDT: f.h, Tunnel: f.h}, nil
}
func (f *signalFactory) Allocate(context.Context) (sessionedge.Lease, error) {
	if f.firstBusy || (f.busy && f.calls > 1) {
		return nil, session.ErrBusy
	}
	return f, nil
}
func (f *signalFactory) Stop(context.Context) error {
	f.stops++
	if f.busy {
		return nil
	}
	return session.ErrReleaseAbandoned
}

type signalHandle struct{ notices chan session.Event }

func (h *signalHandle) Dial(context.Context, sessionedge.Lease) (sessionedge.Transport, error) {
	return h, nil
}
func (h *signalHandle) Handshake(context.Context, sessionedge.Transport) (sessionedge.Transport, error) {
	return h, nil
}
func (h *signalHandle) Create(context.Context, sessionedge.Transport) (sessionedge.Transport, error) {
	return h, nil
}
func (h *signalHandle) Start(context.Context, sessionedge.Transport) (sessionedge.Channels, error) {
	return h, nil
}
func (h *signalHandle) Close() error                 { return nil }
func (h *signalHandle) Events() <-chan session.Event { return h.notices }
func (h *signalHandle) TryOpen(uint64) error         { return nil }
func (h *signalHandle) TryClose(uint64)              {}

type signalSink struct {
	h         *signalHandle
	triggered bool
	joinedAt  time.Time
	busy      bool
	firstBusy bool
}

func (*signalSink) Try(session.Action) error { return nil }
func (s *signalSink) Observe(_ session.Event, before, after session.Snapshot, _ core.Instant) {
	if after.State == session.Ready && !s.triggered && !s.firstBusy {
		s.triggered = true
		s.h.notices <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
	}
	if before.State == session.Releasing && after.State == session.Backoff && (!s.busy || s.firstBusy || after.Attempt == 2) {
		s.joinedAt = time.Now()
		fmt.Println("REVIEW_BACKOFF")
	}
}
func TestActualSignalHelper(t *testing.T) {
	mode := os.Getenv("REVIEW_SIGNAL_HELPER")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	h := &signalHandle{notices: make(chan session.Event, 1)}
	f := &signalFactory{h: h, busy: mode != "transport", firstBusy: mode == "first-busy"}
	sink := &signalSink{h: h, busy: f.busy, firstBusy: f.firstBusy}
	cfg := session.DefaultConfig()
	cfg.RetryBusy = true
	cfg.BackoffMin = 3 * time.Second
	cfg.BackoffMax = 3 * time.Second
	err := sessionedge.Run(ctx, cfg, zeroRetryRNG{}, f, nil, sink)
	if !errors.Is(err, context.Canceled) || (f.busy && err != context.Canceled) {
		t.Fatalf("signal outcome=%v", err)
	}
	wantCalls := 1
	if f.busy && !f.firstBusy {
		wantCalls = 2
	}
	wantStops := 1
	if f.firstBusy {
		wantStops = 0
	}
	if f.calls != wantCalls || f.stops != wantStops {
		t.Fatalf("allocation attempts=%d stops=%d, want %d/%d", f.calls, f.stops, wantCalls, wantStops)
	}
	if d := time.Since(sink.joinedAt); d > time.Second {
		t.Fatalf("backoff signal exit=%v, want <1s", d)
	}
	t.Logf("signal joined in %v; allocations=%d stops=%d", time.Since(sink.joinedAt), f.calls, f.stops)
}
