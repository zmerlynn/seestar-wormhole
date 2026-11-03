package browse

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"sync"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/remote"
)

func newPool(d *dialer) (*Sessions, *logBuffer) {
	log, buf := newLog()
	return NewSessions(context.Background(), d.dial, log), buf
}

func TestGetSharesOneDial(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{fsys: fstest.MapFS{}, gate: make(chan struct{})}
		p, _ := newPool(d)
		defer p.Close()
		var wg sync.WaitGroup
		leases := make(chan *Lease, 10)
		for range 10 {
			wg.Go(func() {
				l, err := p.Get(context.Background())
				if err != nil {
					t.Errorf("Get: %v", err)
					return
				}
				leases <- l
			})
		}
		ctx, cancel := context.WithCancel(context.Background())
		gone := make(chan error)
		go func() {
			_, err := p.Get(ctx)
			gone <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-gone; !errors.Is(err, context.Canceled) {
			t.Errorf("canceled waiter: Get = %v, want context.Canceled", err)
		}
		close(d.gate)
		wg.Wait()
		close(leases)
		for l := range leases {
			if l.Session() != d.session(0) {
				t.Errorf("a lease is on another session")
			}
			l.Release()
		}
		if n := d.dials(); n != 1 {
			t.Errorf("dials = %d, want 1", n)
		}
	})
}

func TestIdleClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{fsys: fstest.MapFS{}}
		p, logs := newPool(d)
		defer p.Close()
		l := mustGet(t, p, context.Background())
		l.Release()
		time.Sleep(idleTimeout - time.Nanosecond)
		synctest.Wait()
		if d.session(0).closed() {
			t.Fatalf("closed before %v", idleTimeout)
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if !d.session(0).closed() {
			t.Fatalf("open %v after the last release", idleTimeout)
		}
		if logs.count("session closed", "reason=idle") != 1 {
			t.Errorf("logs:\n%s", logs)
		}
		l = mustGet(t, p, context.Background())
		defer l.Release()
		if d.dials() != 2 || l.Session() != d.session(1) {
			t.Errorf("Get after an idle close did not dial again")
		}
	})
}

func TestHeldLeaseKeepsSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{fsys: fstest.MapFS{}}
		p, _ := newPool(d)
		defer p.Close()
		l := mustGet(t, p, context.Background())
		time.Sleep(10 * idleTimeout)
		synctest.Wait()
		if d.session(0).closed() {
			t.Fatal("a held lease did not keep the session")
		}
		l.Release()
	})
}

func TestStaleIdleTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{fsys: fstest.MapFS{}}
		p, _ := newPool(d)
		defer p.Close()
		mustGet(t, p, context.Background()).Release() // 0 s
		time.Sleep(10 * time.Second)
		l := mustGet(t, p, context.Background()) // 10 s
		time.Sleep(10 * time.Second)
		l.Release() // 20 s
		time.Sleep(idleTimeout - time.Nanosecond)
		synctest.Wait()
		if d.session(0).closed() {
			t.Fatal("closed by the timer of the first release")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if !d.session(0).closed() {
			t.Fatal("not closed 30 s after the last release")
		}
	})
}

func TestCloseIsIdempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{fsys: fstest.MapFS{}}
		p, logs := newPool(d)
		l := mustGet(t, p, context.Background())
		p.Close()
		p.Close()
		s := d.session(0)
		if s.n("Close") != 1 || s.ctx.Err() == nil {
			t.Errorf("Close calls = %d, context %v; want 1 and canceled", s.n("Close"), s.ctx.Err())
		}
		l.Fail(io.ErrUnexpectedEOF)
		l.Release()
		if logs.count("session closed", "reason=stopped") != 1 {
			t.Errorf("logs:\n%s", logs)
		}
	})
}

func TestFailedDialCancelsItsContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{fsys: fstest.MapFS{}, err: errors.New("no route")}
		p, logs := newPool(d)
		defer p.Close()
		if _, err := p.Get(context.Background()); err == nil || err.Error() != "no route" {
			t.Fatalf("Get = %v, want the dial error", err)
		}
		synctest.Wait()
		if d.session(0).ctx.Err() == nil {
			t.Error("a failed dial's context is still live")
		}
		if logs.count("connect failed", "no route") != 1 {
			t.Errorf("logs:\n%s", logs)
		}
		d.err = nil
		l := mustGet(t, p, context.Background())
		l.Release()
		if d.dials() != 2 {
			t.Errorf("a failed dial was remembered: dials = %d, want 2", d.dials())
		}
	})
}

// remote.Dial returns a nil *SMB with its error; as a Session that is not
// a nil interface, and the failed dial must not close it.
func TestFailedDialNilPointer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		log, _ := newLog()
		p := NewSessions(context.Background(), func(context.Context) (remote.Session, error) {
			var s *remote.SMB
			return s, errors.New("connection refused")
		}, log)
		defer p.Close()
		if _, err := p.Get(context.Background()); err == nil || err.Error() != "connection refused" {
			t.Fatalf("Get = %v, want the dial error", err)
		}
	})
}

// A dial whose every waiter left still publishes a timed session.
func TestAbandonedDial(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{fsys: fstest.MapFS{}, gate: make(chan struct{})}
		p, _ := newPool(d)
		defer p.Close()
		ctx, cancel := context.WithCancel(context.Background())
		var wg sync.WaitGroup
		for range 3 {
			wg.Go(func() {
				if _, err := p.Get(ctx); !errors.Is(err, context.Canceled) {
					t.Errorf("Get = %v, want context.Canceled", err)
				}
			})
		}
		synctest.Wait()
		cancel()
		wg.Wait()
		close(d.gate)
		synctest.Wait()
		time.Sleep(idleTimeout)
		synctest.Wait()
		s := d.session(0)
		if s.n("Close") != 1 {
			t.Fatalf("session closed %d times, want 1", s.n("Close"))
		}
		if s.ctx.Err() == nil {
			t.Error("the retired session's context is still live")
		}
		l := mustGet(t, p, context.Background())
		defer l.Release()
		if d.dials() != 2 {
			t.Errorf("dials = %d, want 2", d.dials())
		}
	})
}

// A waiter canceled at the instant the dial publishes, in either order,
// leaves a session that closes 30 s after its last use.
func TestPublicationRace(t *testing.T) {
	for _, cancelFirst := range []bool{true, false} {
		synctest.Test(t, func(t *testing.T) {
			d := &dialer{fsys: fstest.MapFS{}, gate: make(chan struct{})}
			p, _ := newPool(d)
			defer p.Close()
			ctx, cancel := context.WithCancel(context.Background())
			got := make(chan *Lease, 1)
			go func() {
				l, _ := p.Get(ctx)
				got <- l
			}()
			synctest.Wait()
			if cancelFirst {
				cancel()
				close(d.gate)
			} else {
				close(d.gate)
				cancel()
			}
			if l := <-got; l != nil {
				l.Release()
			}
			time.Sleep(idleTimeout - time.Nanosecond)
			synctest.Wait()
			if d.session(0).closed() {
				t.Fatalf("cancelFirst=%v: closed early", cancelFirst)
			}
			time.Sleep(time.Nanosecond)
			synctest.Wait()
			if !d.session(0).closed() {
				t.Fatalf("cancelFirst=%v: not closed 30 s after its last use", cancelFirst)
			}
		})
	}
}

func TestLateDialAfterClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{fsys: fstest.MapFS{}, gate: make(chan struct{})}
		p, logs := newPool(d)
		waiter := make(chan error)
		go func() {
			_, err := p.Get(context.Background())
			waiter <- err
		}()
		synctest.Wait()
		p.Close()
		close(d.gate)
		if err := <-waiter; !errors.Is(err, errStopped) {
			t.Errorf("waiter: Get = %v, want errStopped", err)
		}
		synctest.Wait()
		s := d.session(0)
		if s.n("Close") != 1 || s.ctx.Err() == nil {
			t.Errorf("late session: Close calls = %d, context %v; want 1 and canceled", s.n("Close"), s.ctx.Err())
		}
		if _, err := p.Get(context.Background()); !errors.Is(err, errStopped) {
			t.Errorf("Get after Close = %v, want errStopped", err)
		}
		if d.dials() != 1 {
			t.Errorf("dials = %d, want 1", d.dials())
		}
		if logs.count("connect failed") != 0 || logs.count("session opened") != 0 {
			t.Errorf("logs:\n%s", logs)
		}
	})
}

// The dial's 30 s limit is not the session's lifetime.
func TestDialTimerIsNotLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{fsys: fstest.MapFS{}}
		d.setup = func(*spy) { time.Sleep(time.Second) }
		p, _ := newPool(d)
		defer p.Close()
		l := mustGet(t, p, context.Background())
		time.Sleep(90 * time.Second)
		synctest.Wait()
		s := d.session(0)
		if s.ctx.Err() != nil || s.closed() {
			t.Fatalf("leased session at 90 s: context %v, closed %v", s.ctx.Err(), s.closed())
		}
		l.Release()
	})
}

func TestSlowDialTimesOut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{fsys: fstest.MapFS{}}
		d.setup = func(*spy) { time.Sleep(dialTimeout + 5*time.Second) }
		p, logs := newPool(d)
		defer p.Close()
		if _, err := p.Get(context.Background()); err == nil {
			t.Fatal("Get succeeded after the dial timed out")
		}
		synctest.Wait()
		s := d.session(0)
		if s.n("Close") != 1 || s.ctx.Err() == nil {
			t.Errorf("late session: Close calls = %d, context %v; want 1 and canceled", s.n("Close"), s.ctx.Err())
		}
		if logs.count("connect failed") != 1 {
			t.Errorf("logs:\n%s", logs)
		}
	})
}

func TestFail(t *testing.T) {
	quiet := []error{fs.ErrNotExist, remote.ErrNotDir, io.EOF, fs.ErrInvalid, fs.ErrPermission}
	for _, err := range quiet {
		synctest.Test(t, func(t *testing.T) {
			d := &dialer{fsys: fstest.MapFS{}}
			p, _ := newPool(d)
			defer p.Close()
			l := mustGet(t, p, context.Background())
			l.Fail(&fs.PathError{Op: "open", Path: "x", Err: err})
			l.Release()
			if d.session(0).closed() {
				t.Errorf("Fail(%v) retired the session", err)
			}
		})
	}
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{fsys: fstest.MapFS{}}
		p, logs := newPool(d)
		defer p.Close()
		l := mustGet(t, p, context.Background())
		l.Fail(io.ErrUnexpectedEOF)
		if !d.session(0).closed() {
			t.Fatal("Fail(io.ErrUnexpectedEOF) kept the session")
		}
		l.Release()
		mustGet(t, p, context.Background()).Release()
		if d.dials() != 2 {
			t.Errorf("dials = %d, want 2", d.dials())
		}
		if logs.count("session closed", "reason=failed") != 1 {
			t.Errorf("logs:\n%s", logs)
		}
	})
}

func TestStaleLease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{fsys: fstest.MapFS{}}
		p, _ := newPool(d)
		defer p.Close()
		old1 := mustGet(t, p, context.Background())
		old2 := mustGet(t, p, context.Background())
		old1.Fail(io.ErrUnexpectedEOF)
		l := mustGet(t, p, context.Background())
		old2.Fail(io.ErrUnexpectedEOF)
		old2.Release()
		old1.Release()
		if d.session(1).closed() || p.leases() != 1 {
			t.Fatalf("a stale lease changed the new session: closed %v, leases %d", d.session(1).closed(), p.leases())
		}
		time.Sleep(10 * idleTimeout)
		synctest.Wait()
		if d.session(1).closed() {
			t.Fatal("a stale Release armed the new session's idle timer")
		}
		l.Release()
		time.Sleep(idleTimeout)
		synctest.Wait()
		if !d.session(1).closed() {
			t.Fatal("not closed 30 s after the last release")
		}
	})
}

func TestGraceRetiresStuckSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{fsys: fstest.MapFS{}}
		p, logs := newPool(d)
		defer p.Close()
		ctx, cancel := context.WithCancel(context.Background())
		l := mustGet(t, p, ctx)
		if l.Context().Done() != nil {
			t.Error("the lease context carries the request's cancellation")
		}
		cancel()
		time.Sleep(cancelGrace - time.Nanosecond)
		synctest.Wait()
		if d.session(0).closed() {
			t.Fatal("retired before the grace ended")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if !d.session(0).closed() || logs.count("session closed", "reason=stuck") != 1 {
			t.Fatalf("not retired as stuck at the grace; logs:\n%s", logs)
		}
		l.Release()
		mustGet(t, p, context.Background()).Release()
		if d.dials() != 2 {
			t.Errorf("dials = %d, want 2", d.dials())
		}
	})
}

func TestReleaseInGraceKeepsSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := &dialer{fsys: fstest.MapFS{}}
		p, _ := newPool(d)
		defer p.Close()
		ctx, cancel := context.WithCancel(context.Background())
		l := mustGet(t, p, ctx)
		cancel()
		time.Sleep(time.Second)
		l.Release()
		time.Sleep(idleTimeout - time.Nanosecond)
		synctest.Wait()
		if d.session(0).closed() {
			t.Fatal("a released lease's grace retired the session")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if !d.session(0).closed() {
			t.Fatal("not closed 30 s after the release")
		}
	})
}
