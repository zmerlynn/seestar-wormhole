package proxy

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"testing"
)

// Cancel after DialPort's early Err check, as it evaluates its blocking select.
type cancelAtSelect struct {
	context.Context
	cancel context.CancelFunc
}

func (c cancelAtSelect) Done() <-chan struct{} { c.cancel(); return c.Context.Done() }

func TestCanceledAdmissionReservations(t *testing.T) {
	canceled := false
	s := &Session{clients: make(map[uint64]*stream), in: make(chan session.Event, 257), done: make(chan struct{}), cancel: func() { canceled = true }}
	// Hold the owner still: canceled registrations must keep their reservations
	// until ClientGone is consumed, even when cancellation races Client submission.
	for i := 0; i < 1000; i++ {
		ctx, stop := context.WithCancel(context.Background())
		c, err := s.DialPort(cancelAtSelect{ctx, stop}, 4700)
		stop()
		if c != nil {
			c.Close()
			t.Fatal("canceled admission returned a stream")
		}
		if !errors.Is(err, context.Canceled) && !errors.Is(err, session.ErrBudget) {
			t.Fatal(err)
		}
		if got, want := len(s.clients), min(i+1, 128); got != want {
			t.Fatalf("canceled admission %d retained %d registrations, want %d until consumption", i, got, want)
		}
	}
	if canceled {
		t.Fatal("canceled admissions exhausted inbox and canceled session")
	}
	if len(s.clients) != 128 {
		t.Fatalf("registrations=%d want 128 until owner consumes cleanup", len(s.clients))
	}
	if len(s.in) != 256 {
		t.Fatalf("commands=%d want 128 Client/ClientGone pairs", len(s.in))
	}
	admitted := map[uint64]bool{}
	for len(s.in) > 0 {
		e := <-s.in
		if e.Kind == session.Client {
			admitted[e.Client] = true
		} else {
			if !admitted[e.Client] {
				t.Fatal("cleanup preceded admission")
			}
			delete(admitted, e.Client)
		}
		s.Observe(e, session.Snapshot{}, session.Snapshot{}, core.Instant(0))
	}
	if len(admitted) != 0 || len(s.clients) != 0 {
		t.Fatal("owner did not reclaim consumed reservations")
	}
}

func TestAlreadyCanceledAdmissionCreatesNoWork(t *testing.T) {
	s := &Session{clients: make(map[uint64]*stream), in: make(chan session.Event, 257), done: make(chan struct{})}
	ctx, stop := context.WithCancel(context.Background())
	stop()
	for range 1000 {
		if _, err := s.DialPort(ctx, 4700); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if len(s.clients) != 0 || len(s.in) != 0 || s.serial != 0 {
		t.Fatal("already-canceled admission consumed owner capacity")
	}
}

// Publish successful admission while the select evaluates cancellation, making
// both branches ready without depending on scheduler luck.
type cancelWithReadyAdmission struct {
	context.Context
	cancel context.CancelFunc
	s      *Session
}

func (c cancelWithReadyAdmission) Done() <-chan struct{} {
	c.s.mu.Lock()
	for _, stream := range c.s.clients {
		stream.result <- nil
	}
	c.s.mu.Unlock()
	c.cancel()
	return c.Context.Done()
}

func TestReadyAdmissionCancellationRace(t *testing.T) {
	for range 100 {
		s := &Session{clients: make(map[uint64]*stream), in: make(chan session.Event, 257), done: make(chan struct{})}
		ctx, stop := context.WithCancel(context.Background())
		c, err := s.DialPort(cancelWithReadyAdmission{ctx, stop, s}, 4700)
		stop()
		if c != nil {
			c.Close()
		}
		if c != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("ready admission ignored cancellation: stream=%v error=%v", c != nil, err)
		}
		if len(s.clients) != 1 || len(s.in) != 2 {
			t.Fatal("canceled ready admission lost cleanup reservation")
		}
	}
}
