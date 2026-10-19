package sessionedge

import (
	"bytes"
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
)

func TestAttackConstructorFailureRacingOwnerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		entered := make(chan struct{})
		returnFailure := make(chan struct{})
		cause := diagnostic.NewTerminal(session.Cloud, "device_offline", session.ErrTransport)
		f := newFake()
		factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
			l, _ := f.New(a, b)
			l.Cloud = allocateFunc(func(context.Context) (Lease, error) { close(entered); <-returnFailure; return nil, cause })
			return l, nil
		})
		sink := &failureObserver{ChannelSink: make(ChannelSink, 16), logger: slog.New(slog.NewJSONHandler(&logs, nil))}
		done := make(chan error, 1)
		go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, factory, nil, sink) }()
		<-entered
		// The operation has its immutable cause already, but its return races exit.
		cancel()
		synctest.Wait()
		close(returnFailure)
		err := <-done
		if len(sink.failed) != 1 || !strings.Contains(logs.String(), `"reason":"device_offline"`) {
			t.Errorf("joined constructor failure vanished: events=%d result=%v logs=%q", len(sink.failed), err, logs.String())
		}
		var terminal *diagnostic.Terminal
		if !errors.As(err, &terminal) {
			t.Errorf("returned result has no terminal: %v", err)
		}
	})
}

func TestAttackDrainPreservesTypedTransportFailure(t *testing.T) {
	ch := make(chan session.Event, 1)
	cause := diagnostic.NewTerminal(session.RDT, "timeout", session.ErrTransport)
	ch <- session.Event{Kind: session.Failed, Layer: session.RDT, Err: cause}
	got := drainTerminal(&eventQueue{events: ch}, 1, session.RDT)
	if p, r := diagnostic.Fields(got, ""); p != "rdt" || r != "timeout" {
		t.Fatalf("joined residual failure=%s/%s (%v), want rdt/timeout", p, r, got)
	}
}

func TestTerminalPriorityRetainsCauseWithStrongerPolicy(t *testing.T) {
	for _, policy := range []error{session.ErrStop, session.ErrUncertain, session.ErrBusy} {
		for _, reversed := range []bool{false, true} {
			cause := diagnostic.NewTerminal(session.RDT, "timeout", session.ErrTransport)
			a, b := error(cause), policy
			if reversed {
				a, b = b, a
			}
			got := terminalPriority(a, b)
			if phase, reason := diagnostic.Fields(got, ""); phase != "rdt" || reason != "timeout" {
				t.Errorf("policy=%v reversed=%v terminal=%s/%s, want rdt/timeout", policy, reversed, phase, reason)
			}
			if !errors.Is(got, policy) {
				t.Errorf("policy=%v reversed=%v outcome=%v lost priority", policy, reversed, got)
			}
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			diagnostic.Log(logger, "layer_failed", "session", cause)
			diagnostic.Log(logger, "layer_failed", "session", got)
			if strings.Count(logs.String(), `"level":"ERROR"`) != 1 {
				t.Errorf("policy=%v reversed=%v lost reporting receipt: %s", policy, reversed, &logs)
			}
		}
	}
}

func TestHandledFailureDoesNotPoisonLaterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
			layers, _ := f.New(a, b)
			layers.Cloud = allocateFunc(func(context.Context) (Lease, error) {
				return nil, diagnostic.NewTerminal(session.Cloud, "device_offline", session.ErrTransport)
			})
			return layers, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sink := &failureObserver{ChannelSink: make(ChannelSink, 16)}
		done := make(chan error, 1)
		go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, factory, nil, sink) }()
		synctest.Wait() // Failure and successful cleanup are handled before cancellation.
		if len(sink.failed) != 1 {
			t.Fatalf("observed failures=%d, want 1", len(sink.failed))
		}
		cancel()
		err := <-done
		var terminal *diagnostic.Terminal
		if !errors.Is(err, context.Canceled) || errors.As(err, &terminal) {
			t.Errorf("later cancellation retained an already handled attempt: %v", err)
		}
	})
}

func TestDrainRetainsUntypedRuntimeOrigin(t *testing.T) {
	ch := make(chan session.Event, 1)
	ch <- session.Event{Kind: session.Failed, Err: session.ErrTimeout}
	got := drainTerminal(&eventQueue{events: ch}, 1, session.DTLS)
	if phase, reason := diagnostic.Fields(got, ""); phase != "dtls" || reason != "timeout" {
		t.Fatalf("residual runtime cause=%s/%s, want dtls/timeout", phase, reason)
	}
	ch <- session.Event{Kind: session.Failed, Err: context.Canceled}
	if got := drainTerminal(&eventQueue{events: ch}, 1, session.DTLS); got != nil {
		t.Fatalf("operator cancellation became a terminal failure: %v", got)
	}
}
