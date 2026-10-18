package sessionedge

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Each seed generates notices on a source independent of worker completions.
// Cancellation holds construction until Release has
// reached the owner, so these notices are genuinely queued behind release.
func TestSeededTerminalNotices(t *testing.T) {
	for seed := uint64(1); seed <= 20; seed++ {
		r := rand.New(rand.NewPCG(seed, 27))
		for _, trigger := range []string{"notice-first", "failure", "timeout", "shutdown"} {
			for _, source := range []string{"established", "partial", "late-success"} {
				if trigger == "notice-first" && source != "established" {
					continue
				}
				terminal := []error{session.ErrBusy, session.ErrUncertain, session.ErrStop, errors.Join(session.ErrBusy, session.ErrUncertain), errors.Join(session.ErrBusy, session.ErrStop)}[r.IntN(5)]
				if r.IntN(2) == 0 {
					terminal = errors.Join(errors.New("private diagnostic"), terminal)
				}
				t.Run(fmt.Sprintf("%d/%s/%s", seed, trigger, source), func(t *testing.T) {
					terminalNoticeScenario(t, trigger, source, terminal)
				})
			}
		}
	}
}

func terminalNoticeScenario(t *testing.T, trigger, source string, terminal error) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		f := newFake()
		entered, canceled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
		notices := make(chan session.Event, 3)
		layer := session.Rendezvous
		if source != "established" {
			layer = session.DTLS
		}
		f.notices[layer] = notices
		factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
			l, _ := f.New(a, b)
			if a != 1 { // A fresh attempt must not inherit the old queue.
				f.notices[layer] = nil
				return l, nil
			}
			l.DTLS = reviewHandshake(func(ctx context.Context, tr Transport) (Transport, error) {
				close(entered)
				if trigger != "failure" && trigger != "notice-first" {
					<-ctx.Done()
					close(canceled)
					<-finish
				}
				// Usage cannot re-enable admission during teardown; supplied stamps are
				// deliberately wrong because the runtime owns attempt identity.
				notices <- session.Event{Kind: session.Usage, Attempt: 99, Bytes: 1, Count: 1}
				notices <- session.Event{Kind: session.Failed, Attempt: 99, Err: terminal}
				if trigger == "notice-first" {
					<-ctx.Done()
				}
				var partial Transport
				if source != "established" {
					partial, _ = f.transport(ctx, session.DTLS)
				}
				if source == "late-success" {
					return partial, nil
				}
				return partial, session.ErrTransport
			})
			return l, nil
		})
		cfg := session.DefaultConfig()
		cfg.DialTimeout = time.Second
		in := make(chan session.Event, 8)
		out := make(ChannelSink, 32)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- Run(ctx, cfg, rng{}, factory, in, out) }()
		<-entered
		if trigger != "failure" && trigger != "notice-first" {
			switch trigger {
			case "timeout":
				<-time.After(cfg.DialTimeout)
			case "shutdown":
				in <- session.Event{Kind: session.Shutdown}
			}
			<-canceled
			synctest.Wait()
			close(finish)
		}
		synctest.Wait()
		<-time.After(3 * time.Second)
		synctest.Wait()
		if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
			t.Fatalf("terminal notice lost or duplicate release: allocations/stops=%d/%d, want 1/1", f.allocated.Load(), f.stopped.Load())
		}
		if trigger != "shutdown" {
			in <- session.Event{Kind: session.Client, Client: 55}
			a := outcome(t, out, session.Reject)
			if errors.Is(terminal, session.ErrUncertain) || errors.Is(terminal, session.ErrStop) {
				if !errors.Is(a.Err, session.ErrUncertain) && !errors.Is(a.Err, session.ErrStop) {
					t.Fatalf("uncertain terminal rejection=%v", a.Err)
				}
			} else if !errors.Is(a.Err, session.ErrBusy) {
				t.Fatalf("busy terminal rejection=%v", a.Err)
			}
			in <- session.Event{Kind: session.Shutdown}
		}
		err := <-done
		if errors.Is(terminal, session.ErrUncertain) || errors.Is(terminal, session.ErrStop) {
			if !errors.Is(err, session.ErrStop) {
				t.Fatalf("uncertain terminal exit=%v", err)
			}
		} else {
			// Successful release must retain the current busy terminal outcome.
			phase, reason := diagnostic.Fields(err, "")
			wantPhase := diagnostic.NewTerminal(layer, "busy", session.ErrBusy).Phase()
			if !errors.Is(err, session.ErrBusy) || phase != wantPhase || reason != "busy" {
				t.Fatalf("shutdown terminal=%s/%s (%v), want %s/busy", phase, reason, err, wantPhase)
			}
		}
		want := int32(1)
		if f.allocated.Load() != want || f.stopped.Load() != want {
			t.Fatalf("final allocations/stops=%d/%d want=%d", f.allocated.Load(), f.stopped.Load(), want)
		}
		for _, tr := range f.resources {
			if tr.closes.Load() != 1 {
				t.Fatalf("resource %p closes=%d", tr, tr.closes.Load())
			}
		}
	})
}
