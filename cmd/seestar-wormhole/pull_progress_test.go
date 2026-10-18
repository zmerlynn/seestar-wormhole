package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/go-json-experiment/json/jsontext"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// Only backend read latency is simulated; copying, hashing, receipts and cleanup
// use production code and temporary local files. All waits use synctest time.
type progressSource struct {
	fs.Fs
	cancel context.CancelFunc
}

func (f progressSource) Features() *fs.Features {
	features := *f.Fs.Features()
	features.ListR = nil
	features.Shutdown = func(context.Context) error { time.Sleep(2 * time.Second); return nil }
	return &features
}

func (f progressSource) NewObject(ctx context.Context, name string) (fs.Object, error) {
	o, err := f.Fs.NewObject(ctx, name)
	if err != nil {
		return nil, err
	}
	return progressObject{Object: o, cancel: f.cancel}, nil
}

type progressObject struct {
	fs.Object
	cancel context.CancelFunc
}

func (o progressObject) Open(ctx context.Context, opts ...fs.OpenOption) (io.ReadCloser, error) {
	r, err := o.Object.Open(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &progressReader{ReadCloser: r, ctx: ctx, cancel: o.cancel}, nil
}

type progressReader struct {
	io.ReadCloser
	ctx     context.Context
	cancel  context.CancelFunc
	started bool
	waited  bool
}

func (r *progressReader) Read(p []byte) (int, error) {
	if !r.started {
		r.started = true
		return r.ReadCloser.Read(p)
	}
	if !r.waited {
		r.waited = true
		time.Sleep(65 * time.Second)
		if r.cancel != nil {
			r.cancel()
		}
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(p)
}

type progressSummaryWriter struct {
	bytes.Buffer
	t *testing.T
	s *pullSessionFixture
}

func (w *progressSummaryWriter) Write(p []byte) (int, error) {
	if w.s.closed != 1 {
		w.t.Error("summary preceded release")
	}
	return w.Buffer.Write(p)
}

func TestPullProgressCommand(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "copy", true: "cancel"}[interrupted], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				root, dest := t.TempDir(), t.TempDir()
				filename := filepath.Join(root, "PRIVATE_FILE.fit")
				if err := os.WriteFile(filename, []byte(strings.Repeat("x", 128*1024)), 0600); err != nil {
					t.Fatal(err)
				}
				stamp := time.Now().Add(-time.Hour)
				if err := os.Chtimes(filename, stamp, stamp); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				s := &pullSessionFixture{}
				out := &progressSummaryWriter{t: t, s: s}
				var logs progressBuffer
				err := runPullWithSource(ctx, []string{"pull", "--progress", "--output=json", dest}, out, &logs, keylog.Open,
					func(context.Context, proxy.Config) (commandSession, error) { return s, nil },
					func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
						src, err := local.NewFs(ctx, "PRIVATE_SOURCE", root, configmap.Simple{})
						f := progressSource{Fs: src}
						if interrupted {
							f.cancel = cancel
						}
						return ctx, f, err
					})
				code, status, reason := 0, "copied", "none"
				if interrupted {
					code, status, reason = 130, "canceled", "canceled"
				}
				if pullCode(err) != code {
					t.Fatalf("exit=%d want=%d logs=%s", pullCode(err), code, &logs)
				}
				summary := clockSummary(t, out.Bytes(), code, status, reason, "none")
				if !interrupted && summary["copied"] != float64(1) {
					t.Fatalf("summary=%v", summary)
				}
				if interrupted && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation=%v", err)
				}
				lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
				updates := 0
				partial := false
				for _, line := range lines {
					if strings.HasPrefix(line, "progress ") {
						updates++
						partial = partial || strings.Contains(line, "bytes=32768/")
						if !strings.Contains(line, "files=0/1") && !strings.Contains(line, "files=0/0") {
							t.Fatalf("unexpected counts: %s", line)
						}
						if !strings.Contains(line, "bytes=") || !strings.Contains(line, "rate=") {
							t.Fatalf("missing counters: %s", line)
						}
					} else if !jsontext.Value([]byte(line)).IsValid() {
						t.Fatalf("corrupt log: %q", line)
					}
				}
				if !partial || updates < 2 || updates > 3 || !strings.HasSuffix(logs.String(), "\n") || strings.ContainsAny(logs.String(), "\r\x1b") || strings.Contains(logs.String(), "PRIVATE_") {
					t.Fatalf("redirected updates=%d logs=%q", updates, logs.String())
				}
			})
		})
	}
}

func TestPullProgressDefaultOff(t *testing.T) {
	for _, flag := range []string{"", "--progress=false"} {
		name := "default"
		if flag != "" {
			name = "explicit-false"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				root, dest := t.TempDir(), t.TempDir()
				args := []string{"pull", "--log-level=error", dest}
				if flag != "" {
					args = append(args, flag)
				}
				var out, logs bytes.Buffer
				err := runPullWithSource(t.Context(), args, &out, &logs, keylog.Open,
					func(context.Context, proxy.Config) (commandSession, error) { return &pullSessionFixture{}, nil },
					func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
						src, err := local.NewFs(ctx, "fixture", root, configmap.Simple{})
						return ctx, src, err
					})
				if err != nil || logs.Len() != 0 {
					t.Fatalf("err=%v logs=%q", err, &logs)
				}
				want := "status=nothing-new copied=0 verified=0 deferred=0 bytes=0 first_scan=true lower=0001-01-01T00:00:00Z upper=" + time.Now().Add(-2*time.Minute).UTC().Format(time.RFC3339Nano) + " reason=none cleanup_reason=none\n"
				if out.String() != want {
					t.Fatalf("stdout=%q want=%q", &out, want)
				}
			})
		})
	}
}
