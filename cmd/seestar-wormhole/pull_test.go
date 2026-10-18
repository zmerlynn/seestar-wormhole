package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestPullParsingAndHelpWithoutResources(t *testing.T) {
	for _, args := range [][]string{{"pull", "--help"}, {"pull", "--version"}, {"pull", "--version", "--transfers=9"}, {"pull", "--settle=-1m", "PRIVATE_DEST"}, {"pull", "--via=PRIVATE_ENDPOINT", "PRIVATE_DEST"}, {"pull", "--only=**", "PRIVATE_DEST"}} {
		var out, logs bytes.Buffer
		err := runWithSession(context.Background(), args, &out, &logs, func(string, *slog.Logger) (*keylog.Writer, error) { t.Fatal("capture opened"); return nil, nil }, func(context.Context, proxy.Config) (commandSession, error) {
			t.Fatal("session opened")
			return nil, nil
		})
		good := args[1] == "--help" || (args[1] == "--version" && len(args) == 2)
		if good && pullCode(err) != 0 {
			t.Fatalf("%v: %v", args, err)
		}
		if !good && pullCode(err) != 2 {
			t.Fatalf("%v: exit=%d err=%v", args, pullCode(err), err)
		}
		if strings.Contains(logs.String(), "PRIVATE_") {
			t.Fatal("argument leak", &logs)
		}
	}
	o, err := parse([]string{"pull", "data", "--only=Dark,Light", "--only=Dark.*", "--bwlimit=1.5MiB/s"}, io.Discard)
	if err != nil || len(o.pull.config.Only) != 2 || o.pull.config.Only[1] != "Dark.*" || o.pull.config.BytesPerSecond != 1572864 {
		t.Fatalf("interspersing/array/rate: %+v %v", o.pull, err)
	}
	for value, want := range map[string]int64{"0": 0, "1bytes/s": 1, "2KiB/s": 2048, "1.5MiB/s": 1572864} {
		got, err := parseBandwidth(value)
		if err != nil || got != want {
			t.Fatalf("%s: %d %v", value, got, err)
		}
	}
	for _, value := range []string{"1", "-1bytes/s", "off", "0KiB/s", "NaNbytes/s", "InfMiB/s"} {
		if _, err := parseBandwidth(value); err == nil {
			t.Errorf("accepted rate %s", value)
		}
	}
}

func TestPullExitClassification(t *testing.T) {
	for _, test := range []struct {
		err  error
		want int
	}{{nil, 0}, {errors.New("network"), 1}, {&argumentError{Option: "--since"}, 2}, {session.ErrBusy, 3}, {pull.ErrLocked, 3}, {&pullOutcome{4, nil}, 4}, {context.Canceled, 130}, {context.DeadlineExceeded, 1}, {errors.Join(session.ErrBusy, session.ErrStop), 1}, {errors.Join(&pullOutcome{4, nil}, session.ErrStop), 1}} {
		if got := pullCode(test.err); got != test.want {
			t.Errorf("%v: exit %d want %d", test.err, got, test.want)
		}
	}
}

type pullSessionFixture struct {
	addr              string
	dialErr, closeErr error
	closed            int
}

func (s *pullSessionFixture) DialPort(ctx context.Context, port uint16) (net.Conn, error) {
	if port != 445 {
		return nil, errors.New("unexpected port")
	}
	if s.dialErr != nil {
		return nil, s.dialErr
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", s.addr)
}
func (s *pullSessionFixture) Close() error             { s.closed++; return s.closeErr }
func (*pullSessionFixture) Done() <-chan struct{}      { return make(chan struct{}) }
func (*pullSessionFixture) DeviceDetail() cloud.Detail { return cloud.Detail{} }
func (*pullSessionFixture) SourceID() string           { return "fixture-source" }

func TestPullLockBusyAndCancellation(t *testing.T) {
	for _, cause := range []error{session.ErrBusy, context.Canceled, context.DeadlineExceeded} {
		var out, logs bytes.Buffer
		s := &pullSessionFixture{dialErr: cause}
		err := runWithSession(context.Background(), []string{"pull", t.TempDir()}, &out, &logs, keylog.Open, func(_ context.Context, cfg proxy.Config) (commandSession, error) {
			if !cfg.HoldOnDemand || cfg.BusyWait != 0 {
				t.Fatal("pull default acquisition policy", cfg)
			}
			return s, nil
		})
		if pullCode(err) != pullCode(cause) || s.closed != 1 {
			t.Fatalf("%v: code=%d closed=%d out=%s", cause, pullCode(err), s.closed, &out)
		}
	}
	dest := t.TempDir()
	d, err := pull.OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var out, logs bytes.Buffer
	err = runWithSession(context.Background(), []string{"pull", "--busy-wait=90s", dest}, &out, &logs, keylog.Open, func(context.Context, proxy.Config) (commandSession, error) {
		t.Fatal("lock refusal contacted device")
		return nil, nil
	})
	if pullCode(err) != 3 || !strings.Contains(out.String(), "status=busy") {
		t.Fatalf("lock code=%d out=%s", pullCode(err), &out)
	}
	assertPullFailureOutput(t, out.String(), logs.String(), "lock_held")
}

func TestPullSMBCommandReleaseAndStatuses(t *testing.T) {
	addr := os.Getenv("SEESTAR_TEST_SAMBA_ADDR")
	if addr == "" {
		t.Skip("container Samba fixture required")
	}
	for _, failRelease := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "release-failure"}[failRelease], func(t *testing.T) {
			dest := t.TempDir()
			run := func(extra ...string) (int, string) {
				var out, logs bytes.Buffer
				s := &pullSessionFixture{addr: addr}
				if failRelease {
					s.closeErr = session.ErrStop
				}
				args := append([]string{"pull", "--settle=0"}, extra...)
				args = append(args, dest)
				err := runWithSession(context.Background(), args, &out, &logs, keylog.Open, func(context.Context, proxy.Config) (commandSession, error) { return s, nil })
				if s.closed != 1 {
					t.Fatalf("close=%d", s.closed)
				}
				return pullCode(err), out.String()
			}
			code, out := run()
			want := 0
			if failRelease {
				want = 1
			}
			if code != want {
				t.Fatalf("exit=%d out=%s", code, out)
			}
			d, err := pull.OpenDestination(dest)
			if err != nil {
				t.Fatal(err)
			}
			if failRelease && len(d.State.Selections) != 0 {
				t.Fatal("release failure advanced checkpoint")
			}
			d.Close()
			if failRelease {
				return
			}
			if !strings.Contains(out, "status=copied copied=2") {
				t.Fatal(out)
			}
			code, out = run("--error-on-empty")
			if code != 4 || !strings.Contains(out, "status=nothing-new") {
				t.Fatalf("empty exit=%d out=%s", code, out)
			}
			code, out = run()
			if code != 0 || !strings.Contains(out, "status=nothing-new") {
				t.Fatalf("no-op exit=%d out=%s", code, out)
			}
			// A recent eligible source is deferred; error-on-empty must not produce 4.
			src := "/tmp/smb-e2e/share/MyWorks/nested/small.fit"
			stat, err := os.Stat(src)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if err = os.Chtimes(src, now, now); err != nil {
				t.Fatal(err)
			}
			code, out = run("--settle=1h", "--error-on-empty")
			os.Chtimes(src, stat.ModTime(), stat.ModTime())
			if code != 0 || !strings.Contains(out, "status=deferred") {
				t.Fatalf("deferred exit=%d out=%s", code, out)
			}
			if _, err = os.Stat(filepath.Join(dest, "SHA256SUMS")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPullRecheckFlag(t *testing.T) {
	o, err := parse([]string{"pull", "--version", "--recheck"}, io.Discard)
	if err != nil || !o.pull.config.Recheck {
		t.Fatalf("recheck option=%+v err=%v", o, err)
	}
}

func TestPullVerifyOption(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		verify bool
	}{
		{[]string{"pull", "--version"}, false},
		{[]string{"pull", "--version", "--verify"}, true},
		{[]string{"pull", "--version", "--verify=false"}, false},
	} {
		o, err := parse(tc.args, io.Discard)
		if err != nil || o.pull == nil || o.pull.config.Verify != tc.verify {
			t.Fatalf("args=%v verify=%t options=%+v err=%v", tc.args, tc.verify, o, err)
		}
	}
}
