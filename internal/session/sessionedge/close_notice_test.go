package sessionedge

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type closeNotices struct {
	Transport
	queue  chan session.Event
	causes []error
}

func (c *closeNotices) Events() <-chan session.Event { return c.queue }
func (c *closeNotices) Close() error {
	err := c.Transport.Close()
	for _, cause := range c.causes {
		c.queue <- session.Event{Kind: session.Failed, Attempt: 99, Err: cause}
	}
	close(c.queue)
	return err
}

type closeCase struct {
	name             string
	rendezvous, dtls []error
	want             error
}

func closeCases() []closeCase {
	return []closeCase{
		{name: "busy", dtls: []error{session.ErrBusy}, want: session.ErrBusy},
		{name: "uncertain", dtls: []error{session.ErrUncertain}, want: session.ErrUncertain},
		{name: "stop", dtls: []error{session.ErrStop}, want: session.ErrStop},
		{name: "busy-uncertain", dtls: []error{session.ErrBusy, session.ErrUncertain}, want: session.ErrUncertain},
		{name: "uncertain-busy", dtls: []error{session.ErrUncertain, session.ErrBusy}, want: session.ErrUncertain},
		{name: "joined", dtls: []error{errors.Join(errors.New("private"), session.ErrBusy, session.ErrUncertain)}, want: session.ErrUncertain},
		{name: "across", rendezvous: []error{session.ErrBusy}, dtls: []error{session.ErrUncertain}, want: session.ErrUncertain},
		{name: "across-reversed", rendezvous: []error{session.ErrUncertain}, dtls: []error{session.ErrBusy}, want: session.ErrUncertain},
		{name: "stop-precedence", rendezvous: []error{session.ErrUncertain}, dtls: []error{errors.Join(session.ErrBusy, session.ErrStop)}, want: session.ErrStop},
	}
}
func closeQueue(tr Transport, causes []error) *closeNotices {
	// One extra slot permits a notice-first busy before any Close-time outcomes.
	return &closeNotices{tr, make(chan session.Event, len(causes)+1), causes}
}
func TestResidualTerminalAfterClose(t *testing.T) {
	for _, c := range closeCases() {
		t.Run(c.name, func(t *testing.T) {
			f := newFake()
			factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
				l, _ := f.New(a, b)
				l.Rendezvous = rendezvousFunc(func(ctx context.Context, lease Lease) (Transport, error) {
					tr, err := f.Dial(ctx, lease)
					return closeQueue(tr, c.rendezvous), err
				})
				l.DTLS = reviewHandshake(func(ctx context.Context, _ Transport) (Transport, error) {
					tr, _ := f.transport(ctx, session.DTLS)
					return closeQueue(tr, c.dtls), session.ErrTransport
				})
				return l, nil
			})
			out, done := make(chan completion, 8), make(chan struct{})
			go attempt(context.Background(), 1, session.DefaultConfig(), factory, out, done)
			<-done
			found, released := false, false
			for len(out) > 0 {
				e := (<-out).event
				if e.Kind == session.Failed && errors.Is(e.Err, c.want) {
					found = true
				}
				if e.Kind == session.Released {
					released = true
					if c.want == session.ErrBusy {
						if e.Err != nil {
							t.Errorf("busy cleanup=%v want nil", e.Err)
						}
					} else if !errors.Is(e.Err, session.ErrStop) {
						t.Errorf("uncertain cleanup=%v want ErrStop", e.Err)
					}
				}
			}
			if !found || !released {
				t.Errorf("Close outcome=%v found=%v released=%v", c.want, found, released)
			}
			if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
				t.Errorf("allocations/stops=%d/%d want 1/1", f.allocated.Load(), f.stopped.Load())
			}
			assertCleanupOrder(t, f.cleanupOrder, f.resources, f, true)
		})
	}
}

type pendingStop struct {
	Lease
	entered, finish chan struct{}
}

func (p pendingStop) Stop(ctx context.Context) error {
	close(p.entered)
	<-p.finish
	return p.Lease.Stop(ctx)
}

func TestSeededCloseTerminalNotices(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		r := rand.New(rand.NewPCG(seed, 37))
		cases := closeCases()
		r.Shuffle(len(cases), func(i, j int) { cases[i], cases[j] = cases[j], cases[i] })
		for _, c := range cases {
			for _, order := range []string{"worker-first", "notice-first"} {
				t.Run(fmt.Sprintf("%d/%s/%s", seed, c.name, order), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						f := newFake()
						entered, publish := make(chan struct{}), make(chan struct{})
						stopping, finish := make(chan struct{}), make(chan struct{})
						factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
							l, _ := f.New(a, b)
							l.Cloud = allocateFunc(func(ctx context.Context) (Lease, error) {
								lease, err := f.Allocate(ctx)
								return pendingStop{lease, stopping, finish}, err
							})
							var rv *closeNotices
							l.Rendezvous = rendezvousFunc(func(ctx context.Context, lease Lease) (Transport, error) {
								tr, err := f.Dial(ctx, lease)
								rv = closeQueue(tr, c.rendezvous)
								return rv, err
							})
							l.DTLS = reviewHandshake(func(ctx context.Context, _ Transport) (Transport, error) {
								close(entered)
								<-publish
								if order == "notice-first" {
									rv.queue <- session.Event{Kind: session.Failed, Err: session.ErrBusy}
									<-ctx.Done()
								}
								tr, _ := f.transport(ctx, session.DTLS)
								return closeQueue(tr, c.dtls), session.ErrTransport
							})
							return l, nil
						})
						in, out := make(chan session.Event, 8), make(ChannelSink, 16)
						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()
						done := make(chan error, 1)
						go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, factory, in, out) }()
						<-entered
						synctest.Wait() // Install the established queue before notice-first delivery.
						close(publish)
						<-stopping
						synctest.Wait()
						close(finish)
						synctest.Wait()
						<-time.After(3 * time.Second)
						synctest.Wait()
						if f.allocated.Load() != 1 {
							t.Errorf("terminal Close notice retried: allocations=%d want 1", f.allocated.Load())
						}
						in <- session.Event{Kind: session.Client, Client: 1}
						a := outcome(t, out, session.Reject)
						want := session.ErrBusy
						if c.want != session.ErrBusy {
							want = session.ErrStop
						}
						if !errors.Is(a.Err, want) {
							t.Errorf("terminal rejection=%v want %v", a.Err, want)
						}
						cancel()
						err := <-done
						if !errors.Is(err, context.Canceled) {
							t.Errorf("exit=%v want cancellation", err)
						}
						if c.want != session.ErrBusy && !errors.Is(err, session.ErrStop) {
							t.Errorf("uncertainty lost at exit: %v", err)
						}
						if c.want == session.ErrBusy && errors.Is(err, session.ErrStop) {
							t.Errorf("busy created cleanup failure: %v", err)
						}
						if f.stopped.Load() != 1 {
							t.Errorf("stops=%d want 1", f.stopped.Load())
						}
						assertCleanupOrder(t, f.cleanupOrder, f.resources, f, true)
					})
				})
			}
		}
	}
}
