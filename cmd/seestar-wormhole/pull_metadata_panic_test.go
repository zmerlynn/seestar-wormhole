// Regression fixtures from review/review-pull-panic/probes/panic_probe_test.go.
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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

const reviewSecret = "REVIEW_SECRET private-review.fit"
const reviewCheckpoint = "2026-01-01T00:00:00Z"

type reviewFS struct {
	fs.Fs
	mode, root string
	socket     net.Conn
	started    chan struct{}
	once       sync.Once
	joined     atomic.Bool
}

func (f *reviewFS) NewObject(ctx context.Context, name string) (fs.Object, error) {
	if f.mode == "buffered-metadata" {
		if strings.HasPrefix(name, "b/") {
			f.once.Do(func() { close(f.started) })
			<-ctx.Done()
			f.joined.Store(true)
			return nil, ctx.Err()
		}
		<-f.started
	}
	o, err := f.Fs.NewObject(ctx, name)
	return &reviewObject{Object: o, f: f}, err
}

type reviewObject struct {
	fs.Object
	f            *reviewFS
	opened       atomic.Bool
	postOpenSize atomic.Int32
	opens        atomic.Int32
}

func (o *reviewObject) Size() int64 {
	if o.f.mode == "reopen-size" && o.opens.Load() >= 2 {
		panic(reviewSecret)
	}
	if o.f.mode == "buffered-metadata" && o.opened.Load() {
		n := o.postOpenSize.Add(1)
		_ = os.WriteFile(filepath.Join(o.f.root, "sizes"), []byte(fmt.Sprint(n)), 0600)
		// manualCopy's Size call is before buffering; local Put's preallocation
		// calls Size again after the AsyncReader has started.
		if n == 2 {
			<-o.f.startedRead()
			panic(reviewSecret)
		}
	}
	return o.Object.Size()
}

// Reader startup is recorded using a per-process channel independent of the
// sibling-worker barrier. No wall-clock sleeps are used for coordination.
var reviewReadStarted = make(chan struct{})
var reviewReadOnce sync.Once

func (*reviewFS) startedRead() <-chan struct{} { return reviewReadStarted }
func (o *reviewObject) Open(ctx context.Context, opts ...fs.OpenOption) (io.ReadCloser, error) {
	o.opened.Store(true)
	o.opens.Add(1)
	recordMetadataReader(o.f.root, "reader-opened")
	if o.f.mode == "buffered-metadata" {
		return reviewReader{ReadCloser: o.f.socket, f: o.f}, nil
	}
	r, err := o.Object.Open(ctx, opts...)
	return reviewReader{ReadCloser: r, f: o.f}, err
}

type reviewReader struct {
	io.ReadCloser
	f *reviewFS
}

func (r reviewReader) Read(p []byte) (int, error) {
	if r.f.mode == "reopen-size" {
		p[0] = 120
		return 1, io.ErrUnexpectedEOF
	}
	if r.f.mode == "buffered-metadata" {
		reviewReadOnce.Do(func() { close(reviewReadStarted) })
	}
	return r.ReadCloser.Read(p)
}
func (r reviewReader) Close() error {
	err := r.ReadCloser.Close()
	recordMetadataReader(r.f.root, "reader-closed")
	return err
}

func recordMetadataReader(root, marker string) {
	f, err := os.OpenFile(filepath.Join(root, marker), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	_, err = f.WriteString("reader\n")
	if err = errors.Join(err, f.Close()); err != nil {
		panic(err)
	}
}

type reviewOwner struct {
	*pullSessionFixture
	f    *reviewFS
	root string
	peer net.Conn
}

func (s *reviewOwner) DialPort(context.Context, uint16) (net.Conn, error) {
	c, p := net.Pipe()
	s.peer = p
	return c, nil
}
func (s *reviewOwner) SourceID() string {
	return "review-source"
}
func (s *reviewOwner) Close() error {
	if s.peer != nil {
		defer s.peer.Close()
	}
	joined := (s.f.mode != "buffered-metadata") || s.f.joined.Load()
	h, err := os.OpenFile(filepath.Join(s.root, "closed"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(h, "close joined=%t\n", joined)
	return errors.Join(err, h.Close(), s.pullSessionFixture.Close())
}
func TestPullMetadataPanicChild(t *testing.T) {
	root := os.Getenv("REVIEW_PANIC_ROOT")
	if root == "" {
		return
	}
	mode := os.Getenv("REVIEW_PANIC_MODE")
	src, err := local.NewFs(t.Context(), "review", filepath.Join(root, "source"), configmap.Simple{})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := local.NewFs(t.Context(), "empty", filepath.Join(root, "empty"), configmap.Simple{})
	if err != nil {
		t.Fatal(err)
	}
	opener := func(src fs.Fs) func(context.Context, func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
		return func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
			return ctx, src, nil
		}
	}
	dest := filepath.Join(root, "destination")
	f := &reviewFS{Fs: src, mode: mode, root: root, started: make(chan struct{})}
	// Use identical provenance for the successful checkpoint-seeding command.
	seed := &reviewOwner{pullSessionFixture: &pullSessionFixture{}, f: &reviewFS{}, root: filepath.Join(root, "seed")}
	_ = os.MkdirAll(seed.root, 0700)
	err = runPullWithSource(t.Context(), []string{"pull", "--until=" + reviewCheckpoint, dest}, io.Discard, io.Discard, keylog.Open, func(context.Context, proxy.Config) (commandSession, error) { return seed, nil }, opener(empty))
	if err != nil {
		t.Fatal(err)
	}
	s := &reviewOwner{pullSessionFixture: &pullSessionFixture{}, f: f, root: root}
	if os.Getenv("REVIEW_PANIC_RELEASE") == "fail" {
		s.closeErr = session.ErrStop
	}
	open := opener(f)
	if mode == "buffered-metadata" {
		open = func(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
			c, err := dial(ctx, "tcp", "fixture")
			f.socket = c
			return ctx, f, err
		}
	}
	err = runPullWithSource(t.Context(), []string{"pull", "--settle=0", "--transfers=2", "--output=" + os.Getenv("REVIEW_PANIC_FORMAT"), dest}, os.Stdout, os.Stderr, keylog.Open, func(context.Context, proxy.Config) (commandSession, error) { return s, nil }, open)
	if !errors.Is(err, diagnostic.ErrPullPanic) {
		t.Fatalf("missing typed panic: %T %v", err, err)
	}
	os.Exit(pullCode(err))
}
func TestPullMetadataPanicReleases(t *testing.T) {
	for _, mode := range []string{"buffered-metadata", "reopen-size"} {
		for _, format := range []string{"text", "json"} {
			for _, release := range []string{"ok", "fail"} {
				t.Run(mode+"/"+format+"/"+release, func(t *testing.T) {
					root := t.TempDir()
					for _, dir := range []string{"empty", "source/a", "source/b"} {
						if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
							t.Fatal(err)
						}
					}
					for _, name := range []string{"a/private-review.fit", "b/sibling.fit"} {
						p := filepath.Join(root, "source", name)
						if err := os.WriteFile(p, bytes.Repeat([]byte("x"), 2*1024*1024), 0600); err != nil {
							t.Fatal(err)
						}
						old := time.Now().Add(-time.Hour)
						if err := os.Chtimes(p, old, old); err != nil {
							t.Fatal(err)
						}
					}
					cmd := pullChildCommand(t, "TestPullMetadataPanicChild")
					cmd.Env = append(os.Environ(), "REVIEW_PANIC_ROOT="+root, "REVIEW_PANIC_MODE="+mode, "REVIEW_PANIC_FORMAT="+format, "REVIEW_PANIC_RELEASE="+release)
					var out, logs bytes.Buffer
					cmd.Stdout = &out
					cmd.Stderr = &logs
					err := cmd.Run()
					var exit *exec.ExitError
					want := 1
					marker, merr := os.ReadFile(filepath.Join(root, "closed"))
					t.Logf("mode=%s exit=%v close=%q close_err=%v stdout=%s", mode, err, marker, merr, &out)
					if !errors.As(err, &exit) || exit.ExitCode() != want {
						t.Errorf("child exit=%v want %d; stderr=%s", err, want, &logs)
					}
					if string(marker) != "close joined=true\n" || merr != nil {
						t.Errorf("missing once-only release after join: %q %v", marker, merr)
					}
					opened, openErr := os.ReadFile(filepath.Join(root, "reader-opened"))
					closed, closeErr := os.ReadFile(filepath.Join(root, "reader-closed"))
					if openErr != nil || closeErr != nil || !bytes.Equal(opened, closed) {
						t.Errorf("reader ownership: opened=%q err=%v closed=%q err=%v", opened, openErr, closed, closeErr)
					}
					cleanup := "none"
					if release == "fail" {
						cleanup = "release_failed"
					}
					output := out.String()
					if format == "json" {
						var summary pullSummary
						dec := jsontext.NewDecoder(&out)
						if err := json.UnmarshalDecode(dec, &summary, json.MatchCaseInsensitiveNames(true)); err != nil {
							t.Errorf("summary decode: %v", err)
						}
						if summary.Status != "error" || summary.Reason != "operation_panic" || summary.ExitCode != 1 || summary.CleanupReason != cleanup {
							t.Errorf("untruthful summary: %+v", summary)
						}
						if err := json.UnmarshalDecode(dec, new(any), json.MatchCaseInsensitiveNames(true)); err != io.EOF {
							t.Errorf("extra output: %v", err)
						}
					} else {
						for _, field := range []string{"status=error", "reason=operation_panic", "cleanup_reason=" + cleanup} {
							if !strings.Contains(out.String(), field) {
								t.Errorf("missing %s: %s", field, &out)
							}
						}
					}
					combined := output + logs.String()
					for _, secret := range []string{reviewSecret, "private-review.fit", root, "stack=", `"stack"`} {
						if strings.Contains(combined, secret) {
							t.Errorf("INFO leak: %s", secret)
						}
					}
					d, err := pull.OpenDestination(filepath.Join(root, "destination"))
					if err != nil {
						t.Fatal(err)
					}
					defer d.Close()
					if len(d.State.Selections) != 1 {
						t.Errorf("lost checkpoint: %v", d.State.Selections)
					}
					for _, s := range d.State.Selections {
						if s.Checkpoint.Format(time.RFC3339) != reviewCheckpoint {
							t.Errorf("checkpoint advanced: %v", s.Checkpoint)
						}
					}
				})
			}
		}
	}
}
