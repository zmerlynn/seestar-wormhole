package ui

// These are the attachment's adapter tests: a fake proxy.Dialer stands in
// for the session, so they prove the host's admission and lifetime rules,
// not the session's demand behavior.

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// fakeConn is a stream a fake dialer hands out. Its Close can be held,
// counts its calls, returns closeErr, and records whether the
// attachments' mutex was held when it ran.
type fakeConn struct {
	net.Conn
	as       *attachments  // if set, Close checks its mutex
	hold     chan struct{} // if not nil, Close waits for it
	entered  chan struct{} // if not nil, Close signals it on entry
	closeErr error
	closes   atomic.Int32
	underMu  atomic.Bool // Close ran with as.mu held
}

func (c *fakeConn) Close() error {
	if c.as != nil {
		if c.as.mu.TryLock() {
			c.as.mu.Unlock()
		} else {
			c.underMu.Store(true)
		}
	}
	if c.entered != nil {
		close(c.entered)
	}
	if c.hold != nil {
		<-c.hold
	}
	c.closes.Add(1)
	return c.closeErr
}

// fakeDialer is a session's dialer. DialPort records its ctx, signals
// entered, then answers with dial, or with a new fakeConn when dial is nil.
type fakeDialer struct {
	entered chan context.Context // if not nil, DialPort sends its ctx on entry
	dial    func(ctx context.Context) (net.Conn, error)
	mu      sync.Mutex
	ctxs    []context.Context
	ports   []uint16
}

func (d *fakeDialer) DialPort(ctx context.Context, port uint16) (net.Conn, error) {
	d.mu.Lock()
	d.ctxs = append(d.ctxs, ctx)
	d.ports = append(d.ports, port)
	d.mu.Unlock()
	if d.entered != nil {
		d.entered <- ctx
	}
	if d.dial != nil {
		return d.dial(ctx)
	}
	return &fakeConn{}, nil
}

// blockUntilDone is a DialPort answer that blocks until its ctx ends.
func blockUntilDone(ctx context.Context) (net.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// attached is a bare attachments with run 1 attached to d.
func attached(t *testing.T, d proxy.Dialer) (*attachments, *attachment, func()) {
	t.Helper()
	as := &attachments{}
	as.begin(t.Context(), 1)
	detach, ok := as.attach(1, d)
	if !ok {
		t.Fatal("attach refused")
	}
	return as, as.cur, detach
}

// drained checks that a's admissions and channels are all gone.
func drained(t *testing.T, as *attachments, a *attachment) {
	t.Helper()
	as.mu.Lock()
	n := len(a.conns)
	as.mu.Unlock()
	if n != 0 {
		t.Errorf("%d channels still tracked", n)
	}
	if r := a.regs.Load(); r != 0 {
		t.Errorf("%d admission registrations left", r)
	}
	joined := make(chan struct{})
	go func() { a.wg.Wait(); close(joined) }()
	select {
	case <-joined:
	case <-time.After(10 * time.Second):
		t.Error("the wait group didn't drain")
	}
}

// dialAsync starts an admission for run and returns its result channel.
func dialAsync(ctx context.Context, as *attachments, run uint64) <-chan error {
	done := make(chan error, 1)
	go func() {
		c, err := as.dial(ctx, run)
		if err == nil {
			c.Close()
		}
		done <- err
	}()
	return done
}

// Each way an admission blocked in DialPort ends maps to its error: Stop
// and detach to errNotConnected, the caller going away to its own ctx
// error, and any other DialPort error, even one Stop raced, unchanged.
// Each leaves nothing tracked.
func TestAdapterAdmissionErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(as *attachments, detach func(), cancel context.CancelFunc, release chan struct{})
		busy bool // DialPort waits for release, then answers ErrBusy
		want error
	}{
		{name: "Stop", end: func(as *attachments, _ func(), _ context.CancelFunc, _ chan struct{}) { as.stop(1) }, want: errNotConnected},
		{name: "detach", end: func(_ *attachments, detach func(), _ context.CancelFunc, _ chan struct{}) { go detach() }, want: errNotConnected},
		{name: "caller canceled", end: func(_ *attachments, _ func(), cancel context.CancelFunc, _ chan struct{}) { cancel() }, want: context.Canceled},
		{name: "busy while Stop races", busy: true, end: func(as *attachments, _ func(), _ context.CancelFunc, release chan struct{}) {
			as.stop(1)
			close(release)
		}, want: session.ErrBusy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			d := &fakeDialer{entered: make(chan context.Context, 1), dial: blockUntilDone}
			if tc.busy {
				d.dial = func(context.Context) (net.Conn, error) {
					<-release
					return nil, session.ErrBusy
				}
			}
			as, a, detach := attached(t, d)
			defer detach()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := dialAsync(ctx, as, 1)
			<-d.entered
			tc.end(as, detach, cancel, release)
			select {
			case err := <-done:
				if !errors.Is(err, tc.want) || tc.want == errNotConnected && err != errNotConnected {
					t.Errorf("dial = %v, want %v", err, tc.want)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the admission didn't return")
			}
			drained(t, as, a)
		})
	}
}

// A conn DialPort returns after Stop or detach is closed and never
// registered. Its close runs outside the attachments' lock, and the
// admission releases its count only after the close has returned, so a
// detach waiting on it doesn't return early. Stop and the live check
// don't wait on the paused admission.
func TestAdapterRejectAfterDialClosesOutsideLock(t *testing.T) {
	for _, how := range []string{"Stop", "detach"} {
		t.Run(how, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				hold := make(chan struct{})
				raw := &fakeConn{hold: hold}
				d := &fakeDialer{dial: func(context.Context) (net.Conn, error) { return raw, nil }}
				as, a, detach := attached(t, d)
				raw.as = as
				paused, resume := make(chan struct{}), make(chan struct{})
				as.decide = func() {
					close(paused)
					<-resume
				}
				done := dialAsync(t.Context(), as, 1)
				<-paused
				var detached atomic.Bool
				if how == "Stop" {
					as.stop(1) // returns during the pause
					if as.live(1) {
						t.Error("live after Stop")
					}
				} else {
					go func() { detach(); detached.Store(true) }()
					synctest.Wait() // detach waits on the admission
				}
				close(resume)
				synctest.Wait() // the rejected conn's Close is held
				if raw.closes.Load() != 0 {
					t.Fatal("the close finished before it was released")
				}
				if detached.Load() {
					t.Error("detach returned before the rejected conn's close")
				}
				close(hold)
				if err := <-done; err != errNotConnected {
					t.Errorf("dial = %v, want errNotConnected", err)
				}
				if how == "Stop" {
					detach()
				}
				synctest.Wait()
				if !detached.Load() && how == "detach" {
					t.Error("detach didn't return after the admission")
				}
				if raw.closes.Load() != 1 {
					t.Errorf("raw conn closed %d times, want 1", raw.closes.Load())
				}
				if raw.underMu.Load() {
					t.Error("the raw conn was closed under the attachments' lock")
				}
				drained(t, as, a)
			})
		})
	}
}

// A conn whose stream's Close blocks holds detach: the SMB session's
// Close runs the stream's Close once, detach's Close waits for it and
// returns its result, and detach returns only once it has finished. The
// live check and Stop don't wait on it.
func TestAdapterBlockedCloseHoldsDetach(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hold, entered := make(chan struct{}), make(chan struct{})
		closeErr := errors.New("synthetic close failure")
		raw := &fakeConn{hold: hold, entered: entered, closeErr: closeErr}
		d := &fakeDialer{dial: func(context.Context) (net.Conn, error) { return raw, nil }}
		as, a, detach := attached(t, d)
		raw.as = as
		c, err := as.dial(t.Context(), 1)
		if err != nil {
			t.Fatal(err)
		}
		first, second := make(chan error, 1), make(chan error, 1)
		go func() { first <- c.Close() }() // the SMB session closing its conn
		<-entered
		var detached atomic.Bool
		go func() { detach(); detached.Store(true) }()
		go func() { second <- c.Close() }()
		synctest.Wait()
		if detached.Load() {
			t.Error("detach returned while the stream's Close was blocked")
		}
		select {
		case err := <-second:
			t.Errorf("a second Close returned %v before the first finished", err)
		default:
		}
		as.stop(1)
		_ = as.live(1)
		close(hold)
		synctest.Wait()
		if !detached.Load() {
			t.Error("detach didn't return once the Close finished")
		}
		for _, ch := range []chan error{first, second} {
			if err := <-ch; err != closeErr {
				t.Errorf("Close = %v, want the first Close's error", err)
			}
		}
		if n := raw.closes.Load(); n != 1 {
			t.Errorf("stream closed %d times, want 1", n)
		}
		if raw.underMu.Load() {
			t.Error("the stream was closed under the attachments' lock")
		}
		drained(t, as, a)
	})
}

// Detach waits for an admission still in DialPort, which then fails
// errNotConnected and closes the stream it got.
func TestAdapterDetachWaitsForAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		raw := &fakeConn{}
		d := &fakeDialer{entered: make(chan context.Context, 1), dial: func(context.Context) (net.Conn, error) {
			<-release // ignores its ctx
			return raw, nil
		}}
		as, a, detach := attached(t, d)
		done := dialAsync(t.Context(), as, 1)
		<-d.entered
		var detached atomic.Bool
		go func() { detach(); detached.Store(true) }()
		synctest.Wait()
		if detached.Load() {
			t.Error("detach returned while an admission was in DialPort")
		}
		close(release)
		if err := <-done; err != errNotConnected {
			t.Errorf("dial = %v, want errNotConnected", err)
		}
		synctest.Wait()
		if !detached.Load() {
			t.Error("detach didn't return after the admission")
		}
		if raw.closes.Load() != 1 {
			t.Errorf("stream closed %d times, want 1", raw.closes.Load())
		}
		drained(t, as, a)
	})
}

// The ctx an admission gives DialPort ends when the admission returns,
// on success and on failure, and on an early detach, while the caller's
// own ctx and its parent stay live.
func TestAdapterAdmissionCtxReleased(t *testing.T) {
	t.Run("success and failure", func(t *testing.T) {
		fail := errors.New("synthetic DialPort failure")
		var failing atomic.Bool
		d := &fakeDialer{dial: func(context.Context) (net.Conn, error) {
			if failing.Load() {
				return nil, fail
			}
			return &fakeConn{}, nil
		}}
		as, a, detach := attached(t, d)
		defer detach()
		parent, cancelParent := context.WithCancel(t.Context())
		defer cancelParent()
		child, cancelChild := context.WithCancel(parent)
		defer cancelChild()
		c, err := as.dial(child, 1)
		if err != nil {
			t.Fatal(err)
		}
		failing.Store(true)
		if _, err := as.dial(child, 1); !errors.Is(err, fail) {
			t.Errorf("failed dial = %v", err)
		}
		d.mu.Lock()
		ctxs := append([]context.Context(nil), d.ctxs...)
		d.mu.Unlock()
		for i, ctx := range ctxs {
			if ctx.Err() == nil {
				t.Errorf("admission %d's ctx is still live", i)
			}
		}
		if parent.Err() != nil || child.Err() != nil {
			t.Error("the caller's ctx ended")
		}
		if !as.live(1) {
			t.Error("the run isn't live")
		}
		if r := a.regs.Load(); r != 0 {
			t.Errorf("%d registrations left", r)
		}
		c.Close()
	})
	t.Run("early detach", func(t *testing.T) {
		d := &fakeDialer{entered: make(chan context.Context, 1), dial: blockUntilDone}
		as, a, detach := attached(t, d)
		parent, cancelParent := context.WithCancel(t.Context())
		defer cancelParent()
		done := dialAsync(parent, as, 1)
		ctx := <-d.entered
		detach()
		if err := <-done; err != errNotConnected {
			t.Errorf("dial = %v, want errNotConnected", err)
		}
		if ctx.Err() == nil {
			t.Error("the admission's ctx is still live")
		}
		if parent.Err() != nil {
			t.Error("the caller's ctx ended")
		}
		drained(t, as, a)
	})
}

// A stale detach, from a run a newer one replaced, leaves the newer run
// attached.
func TestAdapterStaleDetach(t *testing.T) {
	as := &attachments{}
	as.begin(t.Context(), 1)
	detach1, ok := as.attach(1, &fakeDialer{})
	if !ok {
		t.Fatal("run 1 refused")
	}
	as.begin(t.Context(), 2)
	detach2, ok := as.attach(2, &fakeDialer{})
	if !ok {
		t.Fatal("run 2 refused")
	}
	defer detach2()
	detach1()
	if !as.live(2) {
		t.Error("run 2 isn't live after run 1's detach")
	}
	c, err := as.dial(t.Context(), 2)
	if err != nil {
		t.Errorf("dial run 2 = %v", err)
	} else {
		c.Close()
	}
	if _, err := as.dial(t.Context(), 1); err != errNotConnected {
		t.Errorf("dial run 1 = %v, want errNotConnected", err)
	}
}

// The admission dials the device's SMB port.
func TestAdapterDialsPort445(t *testing.T) {
	d := &fakeDialer{}
	as, _, detach := attached(t, d)
	defer detach()
	c, err := as.dial(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if len(d.ports) != 1 || d.ports[0] != 445 {
		t.Errorf("DialPort ports = %v, want [445]", d.ports)
	}
}

// attachRun is a run that attaches d and arms, as runProxy does, until
// its context ends. Its detach runs before it returns; detached closes
// once it has.
func attachRun(d proxy.Dialer, gate <-chan struct{}, refused *atomic.Bool, detached chan struct{}) runFunc {
	return func(ctx context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status, attach func(proxy.Dialer) (func(), bool)) error {
		if gate != nil {
			<-gate
		}
		detach, ok := attach(d)
		if !ok {
			refused.Store(true)
			close(detached)
			return ctx.Err()
		}
		status.MarkArmed()
		<-ctx.Done()
		detach()
		close(detached)
		return nil
	}
}

// Stop revokes an admission blocked in DialPort: the dial fails
// errNotConnected, and Stop and the status don't wait for it. Nothing is
// left tracked.
func TestAdapterStopRevokesBlockedAdmission(t *testing.T) {
	e := newRouteEnv(t)
	d := &fakeDialer{entered: make(chan context.Context, 1), dial: blockUntilDone}
	detached := make(chan struct{})
	e.s.controller.run = attachRun(d, nil, nil, detached)
	if code, body := e.start(testSettings()); code != 200 {
		t.Fatalf("Connect = %d %s", code, body)
	}
	c := e.s.controller
	var id uint64
	e.until("the run to attach", func() bool {
		_, _, n, ok := c.connected()
		id = n
		return ok
	})
	c.files.mu.Lock()
	a := c.files.cur
	c.files.mu.Unlock()
	done := dialAsync(t.Context(), &c.files, id)
	<-d.entered
	stopped := make(chan struct{})
	go func() { c.stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop waited on the admission")
	}
	if code, _ := e.get("/api/status"); code != 200 {
		t.Errorf("status = %d", code)
	}
	select {
	case err := <-done:
		if err != errNotConnected {
			t.Errorf("dial = %v, want errNotConnected", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the admission didn't return after Stop")
	}
	<-detached
	drained(t, &c.files, a)
}

// A Stop before the run attaches refuses the attach: no dialer is
// published, the run stays unconnected, and a dial for it fails.
func TestAdapterStopBeforeAttach(t *testing.T) {
	e := newRouteEnv(t)
	gate, detached := make(chan struct{}), make(chan struct{})
	var refused atomic.Bool
	entered := make(chan struct{})
	run := attachRun(&fakeDialer{}, gate, &refused, detached)
	e.s.controller.run = func(ctx context.Context, s Settings, cr cloud.Credentials, st *proxy.Status, attach func(proxy.Dialer) (func(), bool)) error {
		close(entered)
		return run(ctx, s, cr, st, attach)
	}
	if code, body := e.start(testSettings()); code != 200 {
		t.Fatalf("Connect = %d %s", code, body)
	}
	<-entered
	c := e.s.controller
	c.mu.Lock()
	id := c.runs
	c.mu.Unlock()
	c.stop()
	close(gate)
	<-detached
	if !refused.Load() {
		t.Error("the attach after Stop succeeded")
	}
	if _, _, _, ok := c.connected(); ok {
		t.Error("connected after Stop")
	}
	if _, err := c.files.dial(t.Context(), id); err != errNotConnected {
		t.Errorf("dial = %v, want errNotConnected", err)
	}
}
