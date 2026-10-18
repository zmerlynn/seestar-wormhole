package main

import (
	"bytes"
	"context"
	"golang.org/x/sys/unix"
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
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
)

func TestPullProgressDetectTerminal(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	if !newPullProgressOutput(master).tty {
		t.Fatal("PTY not recognized")
	}
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if newPullProgressOutput(file).tty {
		t.Fatal("regular file recognized as terminal")
	}
}

// Record terminal output while exposing a real PTY descriptor for detection.
type progressTerminalSink struct {
	progressBuffer
	fd uintptr
}

func (s *progressTerminalSink) Fd() uintptr { return s.fd }

type progressTerminalSummary struct {
	bytes.Buffer
	t    *testing.T
	logs *progressTerminalSink
	s    *pullSessionFixture
}

func (w *progressTerminalSummary) Write(p []byte) (int, error) {
	if w.s.closed != 1 || !strings.HasSuffix(w.logs.String(), " \r") {
		w.t.Error("summary preceded release or terminal clearing")
	}
	return w.Buffer.Write(p)
}

func TestPullProgressSummaryAfterTerminalCleanup(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	synctest.Test(t, func(t *testing.T) {
		root, dest := t.TempDir(), t.TempDir()
		logs := &progressTerminalSink{fd: master.Fd()}
		s := &pullSessionFixture{}
		out := &progressTerminalSummary{t: t, logs: logs, s: s}
		err := runPullWithSource(t.Context(), []string{"pull", "--progress", "--output=json", dest}, out, logs, keylog.Open,
			func(context.Context, proxy.Config) (commandSession, error) { return s, nil },
			func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
				time.Sleep(2 * time.Second)
				src, err := local.NewFs(ctx, "fixture", root, configmap.Simple{})
				return ctx, src, err
			})
		if err != nil || !strings.Contains(logs.String(), "\rprogress ") {
			t.Fatalf("err=%v logs=%q", err, logs.String())
		}
		assertJSONSummary(t, out.String(), 0, "nothing-new", "none", "none")
	})
}

func TestPullProgressNarrowTerminal(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 32, Row: 24}); err != nil {
		t.Fatal(err)
	}
	sink := &progressTerminalSink{fd: master.Fd()}
	w := newPullProgressOutput(sink)
	w.update(pull.ProgressSnapshot{})
	const want = "\rprogress files=0/0 bytes=0/0..."
	if sink.String() != want {
		t.Fatalf("narrow display=%q want=%q", sink.String(), want)
	}
	if err := unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 80, Row: 24}); err != nil {
		t.Fatal(err)
	}
	sink.Reset()
	w.update(pull.ProgressSnapshot{})
	const full = "\rprogress files=0/0 bytes=0/0 rate=0B/s"
	if sink.String() != full {
		t.Fatalf("resized display=%q want=%q", sink.String(), full)
	}
}
