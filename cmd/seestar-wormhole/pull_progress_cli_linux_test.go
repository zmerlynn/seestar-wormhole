package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"golang.org/x/sys/unix"
)

// Use the vendored x/sys PTY ioctls: Go's internal/testpty is not importable.
// A child gets the slave as descriptor 2, just as it would from a shell. The
// bootstrap must construct the real logWriter; no injected Fd sink can hide a
// lost capability at that boundary.
func TestPullProgressCLILogWriter(t *testing.T) {
	if mode := os.Getenv("SEESTAR_TEST_PROGRESS_LOG_WRITER"); mode != "" {
		testPullProgressCLILogWriterChild(t, mode == "terminal")
		return
	}
	for _, mode := range []string{"terminal", "redirected"} {
		t.Run(mode, func(t *testing.T) {
			var sink *os.File
			var master *os.File
			var err error
			if mode == "terminal" {
				master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer master.Close()
				if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
					t.Fatal(err)
				}
				n, err := unix.IoctlGetUint32(int(master.Fd()), unix.TIOCGPTN)
				if err != nil {
					t.Fatal(err)
				}
				sink, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if err := unix.IoctlSetWinsize(int(sink.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 32, Row: 24}); err != nil {
					sink.Close()
					t.Fatal(err)
				}
			} else {
				sink, err = os.CreateTemp(t.TempDir(), "stderr")
				if err != nil {
					t.Fatal(err)
				}
			}
			defer sink.Close()
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPullProgressCLILogWriter$")
			cmd.Env = append(os.Environ(), "SEESTAR_TEST_PROGRESS_LOG_WRITER="+mode)
			cmd.Stderr = sink
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("bootstrap child: %v; stdout=%s", err, out)
			}
			assertJSONSummary(t, string(out), 0, "nothing-new", "none", "none")
			var got []byte
			if master != nil {
				// Read exactly the expected two one-second frames and shutdown
				// clear. A deadline also bounds a missing-frame regression.
				const frame = "\rprogress files=0/0 bytes=0/0..."
				want := frame + frame + "\r" + strings.Repeat(" ", 31) + "\r"
				if err := master.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				got = make([]byte, len(want))
				if _, err := io.ReadFull(master, got); err != nil {
					t.Fatalf("terminal read: %v; got=%q", err, got)
				}
				if string(got) != want {
					t.Fatalf("terminal output=%q want=%q", got, want)
				}
			} else {
				got, err = os.ReadFile(sink.Name())
				if err != nil {
					t.Fatal(err)
				}
				const want = "progress files=0/0 bytes=0/0 rate=0B/s\n"
				if string(got) != want {
					t.Fatalf("redirected output=%q want=%q", got, want)
				}
			}
		})
	}
}

func testPullProgressCLILogWriterChild(t *testing.T, tty bool) {
	root, dest := t.TempDir(), t.TempDir()
	var out bytes.Buffer
	synctest.Test(t, func(t *testing.T) {
		code := mainExitCodeWithRunner([]string{"pull", "--progress", "--output=json", dest}, &out, os.Stderr,
			func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
				return context.WithCancel(ctx)
			}, logWriter, func() error { return nil },
			func(ctx context.Context, o options, out, logs io.Writer) error {
				if got := newPullProgressOutput(logs).tty; got != tty {
					t.Fatalf("production logWriter terminal mode=%v want=%v", got, tty)
				}
				return runParsedPullWithSource(ctx, o, out, logs, keylog.Open,
					func(context.Context, proxy.Config) (commandSession, error) { return &pullSessionFixture{}, nil },
					func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
						synctest.Wait()
						time.Sleep(1500 * time.Millisecond)
						src, err := local.NewFs(ctx, "fixture", root, configmap.Simple{})
						return ctx, src, err
					})
			})
		if code != 0 {
			t.Fatalf("bootstrap exit=%d summary=%s", code, out.String())
		}
	})
	assertJSONSummary(t, out.String(), 0, "nothing-new", "none", "none")
	_, _ = os.Stdout.Write(out.Bytes())
	// Keep the testing harness's PASS line out of the CLI stdout assertion.
	os.Exit(0)
}
