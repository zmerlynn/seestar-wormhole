package app

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

// errValuer is a log value that resolves to an error.
type errValuer struct{ err error }

func (v errValuer) LogValue() slog.Value { return slog.AnyValue(v.err) }

// A failed Save, and errors logged under any key, in a group or through
// a LogValuer, put no folder, serial, address or host name in the log,
// Activity or the page's alert, and keep what failed.
func TestFailuresNameNothing(t *testing.T) {
	const serial = "SN-SENTINEL-9"
	private := filepath.Join(t.TempDir(), "FOLDER-SENTINEL-"+serial)
	e, f := newNativeEnv(t)
	f.answer = filepath.Join(private, "missing.txt")
	e.serve("GET", xURL, "")
	if len(f.alerts) != 1 || f.alerts[0] != "Couldn't save x.txt: not found. Try saving it again." {
		t.Errorf("alerts %q", f.alerts)
	}

	file := filepath.Join(private, "a.jpg")
	for _, c := range []struct {
		err  error
		want string
	}{
		{&os.PathError{Op: "open", Path: file, Err: os.ErrPermission}, "permission denied"},
		{&os.PathError{Op: "create", Path: file, Err: syscall.ENOSPC}, "no space left"},
		{&os.LinkError{Op: "rename", Old: file + ".tmp", New: file, Err: syscall.EXDEV}, "invalid cross-device link"},
		{fmt.Errorf("history %s: %w", file, state.ErrLocked), state.ErrLocked.Error()},
		{fmt.Errorf("lock %s: %w", private, state.ErrFolderLocked), state.ErrFolderLocked.Error()},
		{&net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 77), Port: 445}, Err: syscall.ECONNREFUSED}, "connection refused"},
		{&net.DNSError{Err: "no such host", Name: "telescope-sentinel.invalid", Server: "192.0.2.53:53", IsNotFound: true}, "host not found"},
		{fmt.Errorf("telescope %s at %s: %w", serial, "192.0.2.77:445", errors.New("synthetic")), "synthetic"},
	} {
		if got := reason(c.err); got != c.want {
			t.Errorf("reason(%v) = %q, want %q", c.err, got, c.want)
		}
		e.s.copy.log.Warn("synthetic failure", "err", c.err)
		e.s.copy.log.Warn("synthetic failure", "error", errValuer{c.err})
		e.s.copy.log.Warn("synthetic failure", slog.Group("g", "reason", c.err))
		e.s.log.Warn("synthetic failure", "err", c.err)
		e.s.free.record.Warn("synthetic failure", "error", c.err)
	}
	e.s.copy.log.Warn("synthetic failure", "err", "open "+file+": permission denied")
	lines, _ := e.s.copy.activity.lines()
	var activity strings.Builder
	for _, l := range lines {
		activity.WriteString(l.Text + "\n")
	}
	for where, text := range map[string]string{"log": e.logs.String(), "Activity": activity.String()} {
		for _, s := range []string{"FOLDER-SENTINEL", serial, "192.0.2.77", "192.0.2.53", "telescope-sentinel"} {
			if strings.Contains(text, s) {
				t.Errorf("%s holds %q:\n%s", where, s, text)
			}
		}
		for _, s := range []string{"permission denied", "connection refused", "host not found", state.ErrLocked.Error()} {
			if !strings.Contains(text, s) {
				t.Errorf("%s lacks %q:\n%s", where, s, text)
			}
		}
	}
}

// What is safe to show is kept as it is: this app's errors, and the
// telescope connection's, which name no address.
func TestReasonKeepsSafeText(t *testing.T) {
	for _, s := range []string{
		"Not connected. Go to Connection to reconnect",
		SavedLabel + ": dial tcp: name lookup failed",
		errNoFolder.Error(), ErrClaim.Error(),
	} {
		if got := reason(fmt.Errorf("%w", errors.New(s))); got != s {
			t.Errorf("reason = %q, want %q", got, s)
		}
	}
	for _, s := range []string{"a.b", "x/y", `C:\x`, "fe80::1", "u@h", "SN1"} {
		if safeText("at " + s) {
			t.Errorf("%q is safe", s)
		}
	}
}
