package sessionedge

import (
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type reviewHandshake func(context.Context, Transport) (Transport, error)

func (f reviewHandshake) Handshake(c context.Context, tr Transport) (Transport, error) {
	return f(c, tr)
}

func TestLateBusyTransportNotice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
			calls.Add(1)
			f := newFake()
			notices := make(chan session.Event, 1)
			f.notices[session.Rendezvous] = notices
			l, _ := f.New(a, b)
			l.DTLS = reviewHandshake(func(ctx context.Context, tr Transport) (Transport, error) {
				<-ctx.Done()
				notices <- session.Event{Kind: session.Failed, Err: session.ErrBusy}
				return nil, ctx.Err()
			})
			return l, nil
		})
		cfg := session.DefaultConfig()
		cfg.DialTimeout = time.Second
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Run(ctx, cfg, rng{}, factory, nil, make(ChannelSink, 16)) }()
		synctest.Wait()
		<-time.After(3 * time.Second)
		synctest.Wait()
		got := calls.Load()
		cancel()
		<-done
		if got != 1 {
			t.Fatalf("late current-attempt busy notice lost: allocations=%d, want 1 automatically", got)
		}
	})
}

func TestQueuedBusyTransportNotice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
			calls.Add(1)
			f := newFake()
			notices := make(chan session.Event, 1)
			f.notices[session.Rendezvous] = notices
			l, _ := f.New(a, b)
			l.DTLS = reviewHandshake(func(ctx context.Context, tr Transport) (Transport, error) {
				notices <- session.Event{Kind: session.Failed, Err: session.ErrBusy}
				return nil, session.ErrTransport
			})
			return l, nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, factory, nil, make(ChannelSink, 16)) }()
		synctest.Wait()
		<-time.After(2 * time.Second)
		synctest.Wait()
		got := calls.Load()
		cancel()
		<-done
		if got != 1 {
			t.Fatalf("already-queued current-attempt busy notice lost: allocations=%d, want 1 automatically", got)
		}
	})
}
