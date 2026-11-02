package browse

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/remote"
)

// logBuffer collects log lines from any goroutine.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// count is how many lines hold every one of parts.
func (l *logBuffer) count(parts ...string) int {
	n := 0
	for line := range strings.Lines(l.String()) {
		all := true
		for _, p := range parts {
			all = all && strings.Contains(line, p)
		}
		if all {
			n++
		}
	}
	return n
}

func newLog() (*slog.Logger, *logBuffer) {
	var b logBuffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

// spy is a Session over a Fake that counts every call. With block set to
// "Open", "ReadDir" or "FileClose", that call ignores every context and
// waits for the session's Close, as a call to a silent telescope does.
type spy struct {
	fake      *remote.Fake
	ctx       context.Context // the dial's
	block     string
	openDelay time.Duration                        // each Open sleeps this long first, ignoring every context
	seekFault func(offset int64, whence int) error // if not nil, each file Seek's error
	readFault func() error                         // if not nil, called by each file Read first; a non-nil result is its error
	done      chan struct{}
	once      sync.Once

	mu       sync.Mutex
	calls    map[string]int
	closeCtx []error // the file context's error at each file Close
}

func (s *spy) count(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls[name]++
}

func (s *spy) n(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[name]
}

// closed reports whether Close was called.
func (s *spy) closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *spy) Walk(ctx context.Context, skip func(string, bool) bool) ([]remote.Entry, error) {
	s.count("Walk")
	return s.fake.Walk(ctx, skip)
}

func (s *spy) ReadDir(ctx context.Context, dir string) ([]remote.Entry, error) {
	s.count("ReadDir")
	if s.block == "ReadDir" {
		<-s.done
		return nil, fs.ErrClosed
	}
	return s.fake.ReadDir(ctx, dir)
}

func (s *spy) Open(ctx context.Context, p string) (remote.File, error) {
	s.count("Open:" + p)
	s.count("Open")
	if s.block == "Open" {
		<-s.done
		return nil, fs.ErrClosed
	}
	time.Sleep(s.openDelay)
	f, err := s.fake.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	return &spyFile{File: f, s: s, ctx: ctx}, nil
}

func (s *spy) Close() error {
	s.count("Close")
	s.once.Do(func() { close(s.done) })
	return s.fake.Close()
}

type spyFile struct {
	remote.File
	s   *spy
	ctx context.Context
}

func (f *spyFile) Read(p []byte) (int, error) {
	f.s.count("Read")
	if f.s.readFault != nil {
		if err := f.s.readFault(); err != nil {
			return 0, err
		}
	}
	return f.File.Read(p)
}

func (f *spyFile) Seek(offset int64, whence int) (int64, error) {
	f.s.count("Seek")
	if f.s.seekFault != nil {
		if err := f.s.seekFault(offset, whence); err != nil {
			return 0, err
		}
	}
	return f.File.Seek(offset, whence)
}

func (f *spyFile) Close() error {
	f.s.count("FileClose")
	f.s.mu.Lock()
	f.s.closeCtx = append(f.s.closeCtx, f.ctx.Err())
	f.s.mu.Unlock()
	if f.s.block == "FileClose" {
		<-f.s.done
	}
	return f.File.Close()
}

// dialer makes a new spy over fsys for each dial and counts them. With gate
// set, each dial waits for it to close, ignoring its context; with err set,
// each dial fails.
type dialer struct {
	fsys  fs.FS
	gate  chan struct{}
	err   error
	setup func(*spy) // adjusts each new spy

	mu       sync.Mutex
	sessions []*spy
}

func (d *dialer) dial(ctx context.Context) (remote.Session, error) {
	d.mu.Lock()
	s := &spy{fake: remote.NewFake(d.fsys), ctx: ctx, done: make(chan struct{}), calls: map[string]int{}}
	if d.setup != nil {
		d.setup(s)
	}
	d.sessions = append(d.sessions, s)
	d.mu.Unlock()
	if d.gate != nil {
		<-d.gate
	}
	if d.err != nil {
		return nil, d.err
	}
	return s, nil
}

// dials is how many dials have started.
func (d *dialer) dials() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.sessions)
}

// session is the i-th dialed spy.
func (d *dialer) session(i int) *spy {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sessions[i]
}

// leases is the current session's lease count, or -1 without one.
func (p *Sessions) leases() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur == nil {
		return -1
	}
	return p.cur.leases
}

// mustGet takes a lease or fails the test.
func mustGet(t *testing.T, p *Sessions, ctx context.Context) *Lease {
	t.Helper()
	l, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return l
}
