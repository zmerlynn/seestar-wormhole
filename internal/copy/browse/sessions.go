// Package browse serves a read-only web page for looking through the
// telescope's share: the HTTP handler, its embedded page, and the pool that
// keeps one SMB session for all requests.
package browse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/guard"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// Fixed times of the session pool. None is a flag.
const (
	dialTimeout = 30 * time.Second // a dial and mount that takes longer fails
	idleTimeout = 30 * time.Second // an unused session logs off after this
	cancelGrace = 5 * time.Second  // a canceled request's SMB call may run this long
)

var (
	errStopped = errors.New("browse is stopping")
	errRetired = errors.New("the device session was closed; try again")
)

// errNoAnswer is a dial that took longer than dialTimeout. It reads as it
// always has, and classifies as a timeout.
type errNoAnswer struct{}

func (errNoAnswer) Error() string {
	return fmt.Sprintf("connect to the device: no answer after %v", dialTimeout)
}
func (errNoAnswer) Unwrap() error { return context.DeadlineExceeded }

// Reporter gets the outcome of each Browse operation as it ends: op is
// [diagnostic.OpConnect], [diagnostic.OpRecords] or [diagnostic.OpRead],
// and seq its number from Seq, drawn where the outcome was decided. Report
// is called with none of the pool's locks held. A nil Report, or Seq,
// discards them.
type Reporter struct {
	Report func(op string, err error, canceled bool, seq uint64)
	Seq    func() uint64
}

// seq is the next outcome's number, or 0 without Seq.
func (r Reporter) seq() uint64 {
	if r.Seq == nil {
		return 0
	}
	return r.Seq()
}

// send reports an outcome, if there is a Report.
func (r Reporter) send(op string, err error, canceled bool, seq uint64) {
	if r.Report != nil {
		r.Report(op, err, canceled, seq)
	}
}

// Sessions keeps at most one current remote.Session, shared by every request.
// It dials on first use and logs off after idleTimeout without a lease. One
// mutex guards all of its state; no session is closed and no dial is made
// while holding it.
type Sessions struct {
	ctx  context.Context // the pool's; every dial's context derives from it
	stop context.CancelFunc
	dial func(context.Context) (remote.Session, error)
	log  *slog.Logger
	rep  Reporter

	mu      sync.Mutex
	closed  bool
	cur     *session // the current session, or nil
	pending *dialing // the dial in progress, or nil
}

// session is one dialed session and its pool bookkeeping.
type session struct {
	s      remote.Session
	cancel context.CancelFunc // ends the session's context; called when it retires
	leases int
	idle   *time.Timer // armed while leases is zero
}

// dialing is one dial in progress. sess and err are set before done closes.
type dialing struct {
	done chan struct{}
	sess *session
	err  error
}

// Lease is one request's use of a session. The handler releases it exactly
// once, after every SMB call and the file's Close have returned.
type Lease struct {
	p        *Sessions
	s        *session
	ctx      context.Context
	stop     func() bool // unregisters the request context callback
	grace    *time.Timer // armed once the request context ends
	released bool
}

// NewSessions returns an empty pool, which closes as Close does once ctx
// ends, even when retired with a session still leased. dial opens a
// session; the context it is given is the session's whole lifetime. log
// gets the pool's lines; the caller adds the endpoint to it. rep gets each
// dial's outcome, except one that ends as the pool closes.
func NewSessions(ctx context.Context, dial func(context.Context) (remote.Session, error), log *slog.Logger, rep Reporter) *Sessions {
	ctx, stop := context.WithCancel(ctx)
	p := &Sessions{ctx: ctx, stop: stop, dial: dial, log: log, rep: rep}
	// The pool's own end cancels ctx too, which unregisters this from
	// the parent; Close then changes nothing.
	context.AfterFunc(ctx, p.Close)
	return p
}

// Get returns a lease on the current session, dialing one if there is none.
// Concurrent calls share one dial. ctx is the request's: Get returns its
// error if it ends first, and once it ends while the lease is held, the
// lease's session is retired after cancelGrace unless the lease is released.
func (p *Sessions) Get(ctx context.Context) (*Lease, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errStopped
	}
	if p.cur != nil {
		defer p.mu.Unlock()
		return p.lease(ctx, p.cur), nil
	}
	d := p.pending
	if d == nil {
		d = p.startDial()
	}
	p.mu.Unlock()
	select {
	case <-d.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if d.err != nil {
		return nil, d.err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errStopped
	}
	if p.cur != d.sess {
		return nil, errRetired
	}
	return p.lease(ctx, d.sess), nil
}

// startDial starts a dial; p.mu is held. The dial's context lives as long as
// the session it makes, so its 30 s limit is a timer that cancels it, which
// is stopped once the dial returns, and never a context deadline.
func (p *Sessions) startDial() *dialing {
	d := &dialing{done: make(chan struct{})}
	p.pending = d
	dctx, cancel := context.WithCancel(p.ctx)
	timer := time.AfterFunc(dialTimeout, cancel)
	go func() {
		defer guard.Recover(p.log, "connecting to the device")
		var s remote.Session
		err := guard.Run(p.log, "connecting to the device", func() (err error) {
			s, err = p.dial(dctx)
			return err
		})
		if err != nil {
			// remote.Dial's nil *SMB is not a nil Session.
			s = nil
		}
		if !timer.Stop() {
			if err == nil {
				p.closeSession(s)
				s = nil
			}
			err = errNoAnswer{}
		}
		// A request that fails with it fails in connecting; its text is
		// unchanged.
		err = diagnostic.At(diagnostic.OpConnect, err)
		// The outcome is logged before the waiters wake, so it comes
		// before any line of theirs. It is numbered here, under p.mu, and
		// reported after, so it may follow the waiters' lines.
		var sess *session
		p.mu.Lock()
		p.pending = nil
		closed, seq := p.closed, p.rep.seq()
		switch {
		case p.closed:
			if err == nil {
				err = errStopped
			}
		case err == nil:
			sess = &session{s: s, cancel: cancel}
			p.cur = sess
			p.armIdle(sess)
			p.log.Info("session opened")
		default:
			p.log.Warn("connect failed", append(diagnostic.OpKeys(diagnostic.SourceBrowse, diagnostic.OpConnect, err), "error", err)...)
		}
		d.sess, d.err = sess, err
		close(d.done)
		p.mu.Unlock()
		// A dial that ends as the pool closes neither failed nor
		// succeeded.
		if !closed {
			p.rep.send(diagnostic.OpConnect, err, false, seq)
		}
		if sess == nil {
			if s != nil {
				p.closeSession(s)
			}
			cancel()
		}
	}()
	return d
}

// lease takes a lease on s; p.mu is held.
func (p *Sessions) lease(ctx context.Context, s *session) *Lease {
	s.leases++
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	l := &Lease{p: p, s: s, ctx: context.WithoutCancel(ctx)}
	l.stop = context.AfterFunc(ctx, func() {
		defer guard.Recover(p.log, "watching a request")
		p.mu.Lock()
		defer p.mu.Unlock()
		if !l.released {
			l.grace = time.AfterFunc(cancelGrace, func() {
				defer guard.Recover(p.log, "retiring a stuck device session")
				p.retire(s, "stuck", func() bool { return !l.released })
			})
		}
	})
	return l
}

// armIdle arms s's idle timer; p.mu is held. The callback closes s only if
// this timer is still the armed one, s is still current and unleased, since
// Stop cannot retract a callback that has already started.
func (p *Sessions) armIdle(s *session) {
	var t *time.Timer
	t = time.AfterFunc(idleTimeout, func() {
		defer guard.Recover(p.log, "closing an idle device session")
		p.retire(s, "idle", func() bool { return s.idle == t && s.leases == 0 })
	})
	s.idle = t
}

// retire detaches s if it is still current and ok, called under p.mu,
// agrees, then closes it outside the lock. A retired pool's session (see
// [Sessions.Retire]) is its last, so detaching it ends the pool.
func (p *Sessions) retire(s *session, reason string, ok func() bool) {
	p.mu.Lock()
	if p.cur != s || !ok() {
		p.mu.Unlock()
		return
	}
	p.cur = nil
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	closed := p.closed
	p.mu.Unlock()
	p.end(s, reason)
	if closed {
		p.stop()
	}
}

// end closes a detached session and ends its context, even if Close panics.
func (p *Sessions) end(s *session, reason string) {
	p.closeSession(s.s)
	s.cancel()
	p.log.Info("session closed", "reason", reason)
}

// closeSession closes s. A panic in Close is logged and recovered, so the
// caller's cleanup still runs: a timer's, a dial's, or a multipart
// response's goroutine may be the one closing it.
func (p *Sessions) closeSession(s remote.Session) {
	_ = guard.Run(p.log, "closing the device session", s.Close)
}

// Close closes the current session and cancels a dial in progress. Later
// Gets fail without dialing, a dial that still succeeds is closed, and Fail
// and Release change nothing. Close may be called more than once, and
// after Retire.
func (p *Sessions) Close() {
	p.shut(false)
}

// shut is Close, or Retire when leased. A Close after Retire closes a
// session still leased at once.
func (p *Sessions) shut(leased bool) {
	p.mu.Lock()
	if p.closed && (leased || p.cur == nil) {
		p.mu.Unlock()
		return
	}
	p.closed = true
	s := p.cur
	if leased && s != nil && s.leases > 0 {
		// The last Release ends s and the pool. No dial is in progress
		// while a session is current.
		p.mu.Unlock()
		return
	}
	p.cur = nil
	if s != nil && s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	p.mu.Unlock()
	if s != nil {
		p.end(s, "stopped")
	}
	p.stop()
}

// Retire ends the pool as Close does, except that a session leased now
// stays open until its last lease is released, so a download in progress
// finishes; that release closes it. Later Gets fail without dialing, a
// dial in progress is canceled at once, and Retire after Close, or again,
// changes nothing.
func (p *Sessions) Retire() {
	p.shut(true)
}

// Session is the leased session.
func (l *Lease) Session() remote.Session { return l.s.s }

// Context is for every SMB call made with the lease: it has the request's
// values but not its cancellation, so a call is never abandoned while the
// device may still answer it. The session's own context still ends it.
func (l *Lease) Context() context.Context { return l.ctx }

// Fail reports an error from an SMB call. An error that says nothing about
// the connection is ignored; any other retires the lease's session if it is
// still current, so the next Get dials again.
func (l *Lease) Fail(err error) {
	for _, quiet := range []error{fs.ErrNotExist, fs.ErrInvalid, fs.ErrPermission, io.EOF, remote.ErrNotDir} {
		if errors.Is(err, quiet) {
			return
		}
	}
	l.p.retire(l.s, "failed", func() bool { return true })
}

// Release ends the lease. The last release on the current session arms its
// idle timer, or, once the pool is retired, closes it and ends the pool; a
// lease whose session is no longer current changes nothing.
func (l *Lease) Release() {
	p := l.p
	p.mu.Lock()
	if l.released {
		p.mu.Unlock()
		return
	}
	l.released = true
	l.stop()
	if l.grace != nil {
		l.grace.Stop()
	}
	if p.cur != l.s {
		p.mu.Unlock()
		return
	}
	l.s.leases--
	if l.s.leases > 0 {
		p.mu.Unlock()
		return
	}
	if !p.closed {
		p.armIdle(l.s)
		p.mu.Unlock()
		return
	}
	p.cur = nil
	p.mu.Unlock()
	p.end(l.s, "stopped")
	p.stop()
}
