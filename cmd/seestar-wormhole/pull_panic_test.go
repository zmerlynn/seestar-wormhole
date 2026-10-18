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

const panicPrivateValue = "PRIVATE_PANIC_VALUE private-source.fit"
const panicCheckpoint = "2026-01-01T00:00:00Z"

type panicPullFs struct {
	fs.Fs
	phase   string
	started chan struct{}
	joined  atomic.Bool
	socket  net.Conn
}

func (f *panicPullFs) fault(ctx context.Context, name string) {
	if strings.HasPrefix(name, "a") {
		<-f.started
		panic(panicPrivateValue)
	}
	close(f.started)
	if f.socket != nil {
		// Models SMB calls whose context is used only while connecting.
		_, _ = f.socket.Read(make([]byte, 1))
	} else {
		<-ctx.Done()
	}
	f.joined.Store(true)
}

func (f *panicPullFs) List(ctx context.Context, name string) (fs.DirEntries, error) {
	if f.phase == "inventory" && name != "" {
		f.fault(ctx, name)
		return nil, ctx.Err()
	}
	return f.Fs.List(ctx, name)
}

func (f *panicPullFs) NewObject(ctx context.Context, name string) (fs.Object, error) {
	if f.phase == "worker" || f.phase == "blocked-sibling" {
		f.fault(ctx, name)
		return nil, ctx.Err()
	}
	o, err := f.Fs.NewObject(ctx, name)
	if err == nil && (f.phase == "read" || f.phase == "reread") {
		return panicPullObject{Object: o}, nil
	}
	return o, err
}

func (f *panicPullFs) Features() *fs.Features {
	features := *f.Fs.Features()
	features.ListR = nil
	features.Shutdown = func(context.Context) error {
		if f.phase == "shutdown" {
			panic(panicPrivateValue)
		}
		return nil
	}
	return &features
}

type panicPullObject struct{ fs.Object }

func (o panicPullObject) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	r, err := o.Object.Open(ctx, options...)
	return panicPullReader{ReadCloser: r}, err
}

type panicPullReader struct{ io.ReadCloser }

func (r panicPullReader) Read([]byte) (int, error) { panic(panicPrivateValue) }

type panicPullSession struct {
	*pullSessionFixture
	source *panicPullFs
	marker string
	peer   net.Conn
}

func (s *panicPullSession) DialPort(ctx context.Context, port uint16) (net.Conn, error) {
	if s.source.phase != "blocked-sibling" {
		return s.pullSessionFixture.DialPort(ctx, port)
	}
	client, peer := net.Pipe()
	s.peer = peer
	return client, nil
}
func (s *panicPullSession) Close() error {
	if s.peer != nil {
		defer s.peer.Close()
	}
	joined := s.source.phase != "worker" && s.source.phase != "inventory" && s.source.phase != "blocked-sibling" || s.source.joined.Load()
	f, err := os.OpenFile(s.marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "close joined=%t\n", joined)
	err = errors.Join(err, f.Close())
	return errors.Join(err, s.pullSessionFixture.Close())
}

// Run the actual command in a process that an uncontained library/worker panic
// can kill. The parent asserts the process exit and durable ownership markers.
func TestPullPanicChild(t *testing.T) {
	root := os.Getenv("SEESTAR_PULL_PANIC_CHILD")
	if root == "" {
		return
	}
	phase := os.Getenv("SEESTAR_PULL_PANIC_PHASE")
	dest := filepath.Join(root, "destination")
	empty, err := local.NewFs(t.Context(), "empty", filepath.Join(root, "empty"), configmap.Simple{})
	if err != nil {
		t.Fatal(err)
	}
	opener := func(source fs.Fs) func(context.Context, func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
		return func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
			return ctx, source, nil
		}
	}
	// Persist an existing checkpoint, so the regression checks preservation, not
	// just an absent checkpoint in a brand new destination.
	err = runPullWithSource(t.Context(), []string{"pull", "--until=" + panicCheckpoint, dest}, io.Discard, io.Discard, keylog.Open,
		func(context.Context, proxy.Config) (commandSession, error) { return &pullSessionFixture{}, nil }, opener(empty))
	if err != nil {
		t.Fatal(err)
	}
	source, err := local.NewFs(t.Context(), "source", filepath.Join(root, "source"), configmap.Simple{})
	if err != nil {
		t.Fatal(err)
	}
	fault := &panicPullFs{Fs: source, phase: phase, started: make(chan struct{})}
	s := &panicPullSession{pullSessionFixture: &pullSessionFixture{}, source: fault, marker: filepath.Join(root, "closed")}
	if os.Getenv("SEESTAR_PULL_PANIC_RELEASE") == "fail" {
		s.closeErr = session.ErrStop
	}
	args := []string{"pull", "--settle=0", "--transfers=2", "--output=" + os.Getenv("SEESTAR_PULL_PANIC_FORMAT"), dest}
	if os.Getenv("SEESTAR_PULL_PANIC_DEBUG") == "true" {
		args = append(args, "--log-level=debug")
	}
	open := opener(fault)
	if phase == "blocked-sibling" {
		open = func(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
			var dialErr error
			fault.socket, dialErr = dial(ctx, "tcp", "fixture")
			return ctx, fault, dialErr
		}
	}
	if phase == "setup" {
		open = func(context.Context, func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
			panic(panicPrivateValue)
		}
	}
	err = runPullWithSource(t.Context(), args, os.Stdout, os.Stderr, keylog.Open,
		func(context.Context, proxy.Config) (commandSession, error) { return s, nil }, open)
	// Inspect the typed result, including when release also failed.
	var detail *diagnostic.PullError
	if !errors.As(err, &detail) || detail.Error() != "operation_panic" {
		t.Fatalf("missing typed panic failure: %T %v", err, err)
	}
	os.Exit(pullCode(err))
}

func TestPullPanicReleases(t *testing.T) {
	for _, phase := range []string{"setup", "inventory", "worker", "blocked-sibling", "read", "reread", "shutdown"} {
		for _, format := range []string{"text", "json"} {
			for _, release := range []string{"ok", "fail"} {
				t.Run(phase+"/"+format+"/"+release, func(t *testing.T) {
					testPullPanicProcess(t, phase, format, release, false)
				})
			}
		}
	}
	t.Run("debug-stack", func(t *testing.T) { testPullPanicProcess(t, "worker", "json", "ok", true) })
}

func testPullPanicProcess(t *testing.T, phase, format, release string, debug bool) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"empty", "source/a", "source/b"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a/private-source.fit", "b/sibling.fit"} {
		filename := filepath.Join(root, "source", name)
		if err := os.WriteFile(filename, panicFixtureBytes(phase), 0600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Hour)
		if err := os.Chtimes(filename, old, old); err != nil {
			t.Fatal(err)
		}
		if phase == "reread" {
			filename = filepath.Join(root, "destination", name)
			if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filename, panicFixtureBytes(phase), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	child := pullChildCommand(t, "TestPullPanicChild")
	child.Env = append(os.Environ(), "SEESTAR_PULL_PANIC_CHILD="+root, "SEESTAR_PULL_PANIC_PHASE="+phase,
		"SEESTAR_PULL_PANIC_FORMAT="+format, "SEESTAR_PULL_PANIC_RELEASE="+release, fmt.Sprintf("SEESTAR_PULL_PANIC_DEBUG=%t", debug))
	var out, logs bytes.Buffer
	child.Stdout, child.Stderr = &out, &logs
	err := child.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Errorf("child exit=%v want 1; stdout=%s stderr=%s", err, &out, &logs)
	}
	marker, err := os.ReadFile(filepath.Join(root, "closed"))
	if err != nil || string(marker) != "close joined=true\n" {
		t.Errorf("ownership marker=%q err=%v want exactly one Close after sibling join", marker, err)
	}
	cleanup := "none"
	if release == "fail" {
		cleanup = "release_failed"
	}
	if format == "json" {
		assertJSONPanicSummary(t, out.String(), cleanup)
	} else {
		for _, field := range []string{"status=error", "reason=operation_panic", "cleanup_reason=" + cleanup} {
			if !strings.Contains(out.String(), field) {
				t.Errorf("summary=%s missing %s", &out, field)
			}
		}
	}
	for _, private := range []string{"PRIVATE_PANIC_VALUE", "private-source.fit", root} {
		if strings.Contains(out.String()+logs.String(), private) {
			t.Errorf("private value leaked: %s", private)
		}
	}
	hasStack := strings.Contains(logs.String(), `"stack"`) || strings.Contains(logs.String(), "stack=")
	if hasStack != debug {
		t.Errorf("debug=%t stack logged=%t: %s", debug, hasStack, &logs)
	}
	d, err := pull.OpenDestination(filepath.Join(root, "destination"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if len(d.State.Selections) != 1 {
		t.Fatalf("selections=%v want one original checkpoint", d.State.Selections)
	}
	if phase == "shutdown" && len(d.State.Files) != 2 {
		t.Errorf("completed receipts lost: %v", d.State.Files)
	}
	for _, selection := range d.State.Selections {
		if selection.Checkpoint.Format(time.RFC3339) != panicCheckpoint {
			t.Errorf("checkpoint advanced: %s", selection.Checkpoint)
		}
	}
}

func assertJSONPanicSummary(t *testing.T, output, cleanup string) {
	t.Helper()
	// Shutdown may follow completed copies; compare outcome fields independently
	// of the retained per-file receipt counts.
	var summary pullSummary
	dec := jsontext.NewDecoder(strings.NewReader(output))
	if err := json.UnmarshalDecode(dec, &summary, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatalf("summary=%s: %v", output, err)
	}
	if err := json.UnmarshalDecode(dec, new(any), json.MatchCaseInsensitiveNames(true)); err != io.EOF {
		t.Fatalf("extra summary output: %s", output)
	}
	if summary.ExitCode != 1 || summary.Status != "error" || summary.Reason != "operation_panic" || summary.CleanupReason != cleanup {
		t.Errorf("summary=%+v want failure/operation_panic/1 cleanup=%s", summary, cleanup)
	}
}

// More than one async-reader buffer ensures read/reread panics occur on the
// library's reader goroutine, not only on our synchronous copy worker.
func panicFixtureBytes(phase string) []byte {
	if phase == "read" || phase == "reread" {
		return bytes.Repeat([]byte("x"), 2*1024*1024)
	}
	return []byte("fixture bytes")
}

func TestPullPanicOutcomePriority(t *testing.T) {
	for _, sibling := range []error{context.Canceled, session.ErrBusy, session.ErrStop, &diagnostic.PullError{Reason: diagnostic.DiskFull}} {
		err := errors.Join(sibling, diagnostic.ErrPullPanic)
		_, reason := diagnostic.Fields(err, "pull")
		if pullCode(err) != 1 || reason != "operation_panic" {
			t.Errorf("sibling=%v exit=%d reason=%s", sibling, pullCode(err), reason)
		}
	}
}
