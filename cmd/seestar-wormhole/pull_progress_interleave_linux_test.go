package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
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

func TestPullProgressLogInterleave(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	for _, mode := range []string{"terminal", "redirected"} {
		for _, format := range []string{"text", "json"} {
			t.Run(mode+"/"+format, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					root, dest := t.TempDir(), t.TempDir()
					clockPutFile(t, root, "a.fit", time.Now().Add(-time.Hour))
					var logs interface {
						io.Writer
						String() string
					} = &progressBuffer{}
					interval := 30 * time.Second
					if mode == "terminal" {
						logs = &progressTerminalSink{fd: master.Fd()}
						interval = time.Second
					}
					listed, proceed, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
					// Keep the real pull parked in its fake backend while logs and
					// progress run on the existing simulated clock.
					defer close(proceed)
					var logger *slog.Logger
					s := &pullSessionFixture{}
					out := &progressSummaryWriter{t: t, s: s}
					done := make(chan error, 1)
					go func() {
						done <- runPullWithSource(t.Context(), []string{"pull", "--progress", "--log-level=info", "--log-format=" + format, "--output=json", dest}, out, logs, keylog.Open,
							func(_ context.Context, cfg proxy.Config) (commandSession, error) {
								logger = cfg.Logger
								return s, nil
							},
							func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
								src, err := local.NewFs(ctx, "fixture", root, configmap.Simple{})
								return ctx, stalledProgressSource{src, listed, proceed, nil, closed}, err
							})
					}()
					<-listed
					synctest.Wait()
					const line = "progress files=0/0 bytes=0/0 rate=0B/s"
					frame, clear := line+"\n", ""
					if mode == "terminal" {
						frame, clear = "\r"+line, "\r"+strings.Repeat(" ", len(line))+"\r"
					}
					want := frame
					if got := logs.String(); got != want {
						t.Fatalf("initial progress=%q want=%q", got, want)
					}
					for i, level := range []slog.Level{slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
						stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
						record := fmt.Sprintf("time=%s level=%s msg=pull_interleave count=%d\n", stamp, level, i+1)
						if format == "json" {
							record = fmt.Sprintf("{\"time\":%q,\"level\":%q,\"msg\":\"pull_interleave\",\"count\":%d}\n", stamp, level.String(), i+1)
						}
						// Use the command's configured slog logger, through startup
						// buffering and progress coordination, rather than raw Write.
						logger.Log(t.Context(), level, "pull_interleave", "count", i+1)
						want += clear + record
						if got := logs.String(); got != want {
							t.Fatalf("%s log spliced into progress: got=%q want=%q", level, got, want)
						}
						time.Sleep(interval)
						synctest.Wait()
						want += frame
						if got := logs.String(); got != want {
							t.Fatalf("progress not redrawn after %s: got=%q want=%q", level, got, want)
						}
					}
					proceed <- struct{}{}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					want += clear
					if got := logs.String(); got != want {
						t.Fatalf("final output left partial progress: got=%q want=%q", got, want)
					}
					if mode == "redirected" && strings.ContainsRune(logs.String(), '\r') {
						t.Fatalf("redirected output contains CR: %q", logs.String())
					}
					summary := clockSummary(t, out.Bytes(), 0, "copied", "none", "none")
					if summary["copied"] != float64(1) {
						t.Fatalf("summary=%v", summary)
					}
				})
			})
		}
	}
}
