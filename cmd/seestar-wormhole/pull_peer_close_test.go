package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Hold eight open SMB files at the first data read. This proves the requested
// concurrency and populates the real rclone pool before teardown starts.
type peerCloseFs struct {
	fs.Fs
	reads          atomic.Int64
	ready          chan struct{}
	shutdown       atomic.Bool
	injected       atomic.Int64
	cause, workErr error
	mixed          bool
	atEcho         bool
}

func (f *peerCloseFs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	entries, err := f.Fs.List(ctx, dir)
	for i, entry := range entries {
		if o, ok := entry.(fs.Object); ok {
			entries[i] = peerCloseObject{o, f}
		}
	}
	return entries, err
}
func (f *peerCloseFs) NewObject(ctx context.Context, name string) (fs.Object, error) {
	o, err := f.Fs.NewObject(ctx, name)
	if err != nil {
		return nil, err
	}
	return peerCloseObject{o, f}, nil
}
func (f *peerCloseFs) Features() *fs.Features {
	features := *f.Fs.Features()
	shutdown := features.Shutdown
	features.Shutdown = func(ctx context.Context) error { f.shutdown.Store(true); return shutdown(ctx) }
	return &features
}

type peerCloseObject struct {
	fs.Object
	f *peerCloseFs
}

func (o peerCloseObject) Open(ctx context.Context, opts ...fs.OpenOption) (io.ReadCloser, error) {
	r, err := o.Object.Open(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &peerCloseReader{ReadCloser: r, f: o.f, ctx: ctx}, nil
}

type peerCloseReader struct {
	io.ReadCloser
	f       *peerCloseFs
	ctx     context.Context
	started bool
}

func (r *peerCloseReader) Read(p []byte) (int, error) {
	if !r.started {
		r.started = true
		if r.f.reads.Add(1) == 8 {
			close(r.f.ready)
		}
		select {
		case <-r.f.ready:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
	}
	if r.f.workErr != nil {
		return 0, r.f.workErr
	}
	return r.ReadCloser.Read(p)
}

type peerCloseConn struct {
	net.Conn
	owner  *peerCloseFs
	writes int
}

func (c *peerCloseConn) Write(p []byte) (int, error) {
	if c.owner.shutdown.Load() {
		c.writes++
		// Echo's header/body have completed. Close exactly at the first unmount
		// write, so the peer races teardown rather than any copy/verification.
		if c.writes == 3 && !c.owner.atEcho || c.writes == 1 && c.owner.atEcho {
			index := c.owner.injected.Add(1)
			c.Conn.Close()
			if c.owner.mixed && index > 4 {
				return 0, errors.ErrUnsupported
			}
			return 0, c.owner.cause
		}
	}
	return c.Conn.Write(p)
}

func TestPullCompletedSMBPeerClose(t *testing.T) {
	ep := os.Getenv("SEESTAR_TEST_MANY_SAMBA_ADDR")
	if ep == "" {
		t.Skip("16-file container Samba fixture required")
	}
	for _, test := range []struct {
		name                 string
		close, work, release error
		want                 int
		wantLeaf             string
	}{
		{name: "eof", close: io.EOF},
		{name: "echo-eof", close: io.EOF},
		{name: "echo-independent", close: errors.ErrUnsupported, want: 1, wantLeaf: "unsupported"},
		{name: "echo-timeout", close: context.DeadlineExceeded, want: 1, wantLeaf: "timeout"},
		{name: "already-closed", close: net.ErrClosed},
		{name: "mixed-pool", close: io.EOF, want: 1, wantLeaf: "unsupported"},
		{name: "independent-shutdown", close: errors.ErrUnsupported, want: 1, wantLeaf: "unsupported"},
		{name: "shutdown-timeout", close: context.DeadlineExceeded, want: 1, wantLeaf: "timeout"},
		{name: "release-failed", close: io.EOF, release: session.ErrStop, want: 1},
		{name: "release-cancel-sibling", close: io.EOF, release: errors.Join(context.Canceled, errors.ErrUnsupported), want: 1},
		{name: "release-timeout-sibling", close: io.EOF, release: errors.Join(context.Canceled, context.DeadlineExceeded), want: 1},
		{name: "unverified", close: io.EOF, work: &diagnostic.PullError{Reason: diagnostic.VerifyMismatch}, want: 1},
		{name: "write-error", close: io.EOF, work: &diagnostic.PullError{Reason: diagnostic.DiskFull}, want: 1},
	} {
		for _, via := range []bool{false, true} {
			if via && test.release != nil {
				continue
			}
			t.Run(fmt.Sprintf("%s/via=%t", test.name, via), func(t *testing.T) {
				dest := t.TempDir()
				args := []string{"pull", "--settle=0", "--verify", "--transfers=8", "--timeout=20s", dest}
				if via {
					args = append(args, "--via="+signalSMBProxy(t, ep))
				}
				s := &pullSessionFixture{addr: ep, closeErr: test.release}
				f := &peerCloseFs{ready: make(chan struct{}), cause: test.close, workErr: test.work, mixed: test.name == "mixed-pool", atEcho: strings.HasPrefix(test.name, "echo-")}
				var out, logs bytes.Buffer
				err := runPullWithSource(t.Context(), args, &out, &logs, keylog.Open, func(context.Context, proxy.Config) (commandSession, error) {
					if via {
						t.Fatal("via constructed a session")
					}
					return s, nil
				}, func(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
					ctx, source, err := pull.OpenSMB(ctx, func(ctx context.Context, network, address string) (net.Conn, error) {
						c, err := dial(ctx, network, address)
						if err != nil {
							return nil, err
						}
						return &peerCloseConn{Conn: c, owner: f}, nil
					})
					f.Fs = source
					return ctx, f, err
				})
				if pullCode(err) != test.want {
					t.Fatalf("exit=%d want=%d cause=%T %v stdout=%s logs=%s", pullCode(err), test.want, err, err, &out, &logs)
				}
				if test.wantLeaf != "" {
					var graphHasPeerClosed, graphHasWantLeaf bool
					for _, cause := range diagnostic.Causes(err) {
						graphHasPeerClosed = graphHasPeerClosed || cause.Reason == "peer_closed"
						graphHasWantLeaf = graphHasWantLeaf || cause.Reason == test.wantLeaf
					}
					if !graphHasWantLeaf {
						t.Fatalf("returned error graph lacks %q: %+v", test.wantLeaf, diagnostic.Causes(err))
					}
					var final struct {
						Phase  string   `json:"phase"`
						Leaves []string `json:"leaf_causes"`
					}
					for _, line := range strings.Split(logs.String(), "\n") {
						if json.Unmarshal([]byte(line), &final, json.MatchCaseInsensitiveNames(true)) == nil && final.Leaves != nil {
							break
						}
					}
					if final.Phase != "smb_shutdown" || !strings.Contains(strings.Join(final.Leaves, ","), test.wantLeaf) || (graphHasPeerClosed && !strings.Contains(strings.Join(final.Leaves, ","), "peer_closed")) {
						t.Fatalf("shutdown diagnostics phase=%q leaves=%v want leaf %q peer_closed=%t: %s", final.Phase, final.Leaves, test.wantLeaf, graphHasPeerClosed, &logs)
					}
				}
				if f.reads.Load() < 8 {
					t.Fatalf("only %d simultaneous reads", f.reads.Load())
				}
				if test.work == nil && f.injected.Load() < 8 {
					t.Fatalf("peer close not exercised on eight pooled connections: %d", f.injected.Load())
				}
				d, err := pull.OpenDestination(dest)
				if err != nil {
					t.Fatal(err)
				}
				defer d.Close()
				if test.want == 0 {
					if !strings.Contains(out.String(), "copied=16 verified=16") || len(d.State.Files) != 16 || len(d.State.Selections) != 1 {
						t.Fatalf("incomplete success: %s state=%+v", &out, d.State)
					}
					for name := range d.State.Files {
						data, err := os.ReadFile(filepath.Join(dest, name))
						if err != nil || !bytes.Equal(data, make([]byte, 131072)) {
							t.Fatalf("file %q not identical: %v", name, err)
						}
					}
				} else if len(d.State.Selections) != 0 {
					t.Fatal("failure committed checkpoint")
				}
				wantReleased := 1
				if via {
					wantReleased = 0
				}
				if s.closed != wantReleased {
					t.Fatalf("releases=%d want=%d", s.closed, wantReleased)
				}
			})
		}
	}
}
