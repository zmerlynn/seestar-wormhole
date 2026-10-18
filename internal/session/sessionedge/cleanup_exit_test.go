package sessionedge

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"testing"
	"testing/synctest"
)

func TestEveryExitSurfacesCleanupFailure(t *testing.T) {
	for _, exit := range []string{"shutdown", "cancel", "input", "sink", "panic", "stopped-sink", "stopped-panic"} {
		for _, cleanup := range []string{"stop", "close", "close-panic"} {
			t.Run(exit+"/"+cleanup, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					f := newFake()
					if cleanup == "close" {
						f.failClose = true
					} else if cleanup == "close-panic" {
						f.panicCloseAt = int(session.DTLS)
					} else {
						f.stuckStop = true
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					in := make(chan session.Event, 8)
					out := make(chan session.Action, 16)
					done := make(chan error, 1)
					sink := sinkFunc(func(a session.Action) error {
						if (a.Kind == session.Disconnect && (exit == "sink" || exit == "panic")) || (a.Kind == session.Stopped && (exit == "stopped-sink" || exit == "stopped-panic")) {
							if exit == "panic" || exit == "stopped-panic" {
								panic("secret output panic")
							}
							return errors.New("secret output error")
						}
						out <- a
						return nil
					})
					go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, f, in, sink) }()
					in <- session.Event{Kind: session.Client, Client: 1}
					outcome(t, out, session.Admit)
					switch exit {
					case "cancel":
						cancel()
					case "input":
						close(in)
					default:
						in <- session.Event{Kind: session.Shutdown}
					}
					err := <-done
					if !errors.Is(err, session.ErrStop) {
						t.Fatalf("cleanup failure lost on %s cleanup=%v: %v", exit, cleanup, err)
					}
					original := map[string]error{"cancel": context.Canceled, "input": ErrInputClosed, "sink": ErrAdapter, "panic": ErrPanic, "stopped-sink": ErrAdapter, "stopped-panic": ErrPanic}[exit]
					if original != nil && !errors.Is(err, original) {
						t.Fatalf("original exit lost: %v want %v", err, original)
					}
					if f.stopped.Load() != 1 {
						t.Fatalf("stops=%d", f.stopped.Load())
					}
					for l := session.Rendezvous; l < session.LayerCount; l++ {
						if f.closed[l].Load() != 1 {
							t.Fatalf("close %v count=%d", l, f.closed[l].Load())
						}
					}
				})
			})
		}
	}
}

func TestPreviouslyProcessedCleanupFailureSurvivesExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		f.panicCloseAt = int(session.RDT)
		f.notices[session.DTLS] = make(chan session.Event, 1)
		ctx, cancel := context.WithCancel(context.Background())
		in := make(chan session.Event, 8)
		out := make(ChannelSink, 16)
		done := make(chan error, 1)
		go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, f, in, out) }()
		in <- session.Event{Kind: session.Client, Client: 1}
		outcome(t, out, session.Admit)
		f.notices[session.DTLS] <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
		synctest.Wait()
		cancel()
		err := <-done
		if !errors.Is(err, session.ErrStop) || !errors.Is(err, context.Canceled) {
			t.Fatalf("processed cleanup failure lost: %v", err)
		}
	})
}
