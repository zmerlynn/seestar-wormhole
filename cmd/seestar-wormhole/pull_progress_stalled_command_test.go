package main

import (
	"bytes"
	"context"
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
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
)

type stalledProgressSource struct {
	fs.Fs
	listed, proceed, reading, closed chan struct{}
}

func (f stalledProgressSource) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	close(f.listed)
	<-f.proceed
	return f.Fs.List(ctx, dir)
}
func (f stalledProgressSource) Features() *fs.Features {
	features := *f.Fs.Features()
	features.ListR = nil
	features.Shutdown = func(context.Context) error { close(f.closed); return nil }
	return &features
}
func (f stalledProgressSource) NewObject(ctx context.Context, name string) (fs.Object, error) {
	o, err := f.Fs.NewObject(ctx, name)
	if err != nil || f.reading == nil {
		return o, err
	}
	return stalledProgressObject{Object: o, reading: f.reading}, nil
}

type stalledProgressObject struct {
	fs.Object
	reading chan struct{}
}

func (o stalledProgressObject) Open(ctx context.Context, opts ...fs.OpenOption) (io.ReadCloser, error) {
	r, err := o.Object.Open(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return stalledProgressReader{ReadCloser: r, ctx: ctx, reading: o.reading}, nil
}

type stalledProgressReader struct {
	io.ReadCloser
	ctx     context.Context
	reading chan struct{}
}

func (r stalledProgressReader) Read([]byte) (int, error) {
	close(r.reading)
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func TestPullProgressStalledCommand(t *testing.T) {
	for _, phase := range []string{"initial", "periodic", "cancel"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				root, dest := t.TempDir(), t.TempDir()
				started := time.Now().UTC()
				clockPutFile(t, root, "a.fit", started.Add(-time.Hour))
				listed, proceed, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var reading chan struct{}
				if phase == "cancel" {
					reading = make(chan struct{})
				}
				sink := newStalledProgressSink(1)
				if phase == "periodic" {
					sink.blockOn = 2
				}
				s := &pullSessionFixture{}
				out := &progressSummaryWriter{t: t, s: s}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				args := []string{"pull", "--progress", "--log-level=error", "--output=json", dest}
				done := make(chan error, 1)
				go func() {
					done <- runPullWithSource(ctx, args, out, sink, keylog.Open,
						func(context.Context, proxy.Config) (commandSession, error) { return s, nil },
						func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
							src, err := local.NewFs(ctx, "fixture", root, configmap.Simple{})
							return ctx, stalledProgressSource{src, listed, proceed, reading, closed}, err
						})
				}()
				synctest.Wait()
				select {
				case <-listed:
				default:
					t.Error("command did not reach copying with stalled initial output")
					close(sink.release)
					<-listed
					close(proceed)
					if phase == "cancel" {
						<-reading
						cancel()
					}
					<-done
					return
				}
				if phase == "periodic" {
					<-time.After(30 * time.Second)
				}
				<-sink.entered
				close(proceed)
				if phase == "cancel" {
					<-reading
					cancel()
				}
				// Only the grace timer can now advance fake time. The sink stays blocked
				// through summary, destination reopen, and all resource assertions.
				err := <-done
				code, status, reason := 0, "copied", "none"
				if phase == "cancel" {
					code, status, reason = 130, "canceled", "canceled"
				}
				summary := clockSummary(t, out.Bytes(), code, status, reason, "none")
				if pullCode(err) != code {
					t.Errorf("exit=%d want=%d", pullCode(err), code)
				}
				if s.closed != 1 {
					t.Errorf("session closes=%d want=1", s.closed)
				}
				select {
				case <-closed:
				default:
					t.Error("source was not shut down")
				}
				if got := time.Since(started); got != map[bool]time.Duration{false: time.Second, true: 31 * time.Second}[phase == "periodic"] {
					t.Errorf("command duration=%v: progress grace should add exactly 1s", got)
				}
				d, openErr := pull.OpenDestination(dest)
				if openErr != nil {
					t.Fatalf("destination lock retained: %v", openErr)
				}
				if phase == "cancel" {
					if len(d.State.Files) != 0 || len(d.State.Selections) != 0 {
						t.Errorf("canceled state=%+v", d.State)
					}
				} else {
					receipt := d.State.Files["a.fit"]
					if len(d.State.Files) != 1 || receipt.Size != 5 || receipt.Check != "size" || len(receipt.SHA256) != 64 {
						t.Errorf("saved receipts=%+v", d.State.Files)
					}
					if len(d.State.Selections) != 1 {
						t.Errorf("saved selections=%+v", d.State.Selections)
					}
					for _, selection := range d.State.Selections {
						if !selection.Checkpoint.Equal(started.Add(-2 * time.Minute)) {
							t.Errorf("checkpoint=%v", selection.Checkpoint)
						}
					}
					if data, readErr := os.ReadFile(filepath.Join(dest, "a.fit")); readErr != nil || string(data) != "a.fit" {
						t.Errorf("copy=%q err=%v", data, readErr)
					}
					if summary["copied"] != float64(1) || summary["bytes"] != float64(5) {
						t.Errorf("summary=%v", summary)
					}
				}
				if err := d.Close(); err != nil {
					t.Error(err)
				}
				// Re-run without progress against a fresh destination at the same fixed
				// selection bound and compare stdout bytes plus exit, including cancellation.
				baselineArgs := []string{"pull", "--log-level=error", "--output=json", "--until=" + started.Add(-2*time.Minute).Format(time.RFC3339Nano), t.TempDir()}
				var baseline, logs bytes.Buffer
				baselineCtx, baselineCancel := context.WithCancel(t.Context())
				defer baselineCancel()
				var baselineReading chan struct{}
				if phase == "cancel" {
					baselineReading = make(chan struct{})
				}
				baselineProceed := make(chan struct{})
				close(baselineProceed)
				baselineDone := make(chan error, 1)
				go func() {
					baselineDone <- runPullWithSource(baselineCtx, baselineArgs, &baseline, &logs, keylog.Open,
						func(context.Context, proxy.Config) (commandSession, error) { return &pullSessionFixture{}, nil },
						func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
							src, err := local.NewFs(ctx, "fixture", root, configmap.Simple{})
							return ctx, stalledProgressSource{src, make(chan struct{}), baselineProceed, baselineReading, make(chan struct{})}, err
						})
				}()
				if phase == "cancel" {
					<-baselineReading
					baselineCancel()
				}
				baselineErr := <-baselineDone
				if baseline.String() != out.String() || pullCode(baselineErr) != code {
					t.Errorf("summary changed: progress=%s baseline=%s err=%v", out.String(), baseline.String(), baselineErr)
				}

				close(sink.release)
				synctest.Wait()
				if phase == "initial" && strings.Contains(sink.text.String(), "bytes=5/") {
					t.Error("queued progress survived shutdown")
				}
			})
		})
	}
}
