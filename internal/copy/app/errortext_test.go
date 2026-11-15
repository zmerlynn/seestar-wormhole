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
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
)

// errValuer is a log value that resolves to an error.
type errValuer struct{ err error }

func (v errValuer) LogValue() slog.Value { return slog.AnyValue(v.err) }

// A failed Save, and errors logged under any key, in a group or through
// a LogValuer, show their own text in the log, Activity and the page's
// alert: the folder, the address or host, and what failed.
func TestFailuresShowTheirText(t *testing.T) {
	private := filepath.Join(t.TempDir(), "FOLDER-SENTINEL-9")
	e, f := newNativeEnv(t)
	f.answer = filepath.Join(private, "missing.txt")
	e.serve("GET", xURL, "")
	const lead, tail = "Couldn't save x.txt: ", ". Try saving it again."
	if len(f.alerts) != 1 || !strings.HasPrefix(f.alerts[0], lead) || !strings.HasSuffix(f.alerts[0], tail) || !strings.Contains(f.alerts[0], private) {
		t.Errorf("alerts %q", f.alerts)
	}

	file := filepath.Join(private, "a.jpg")
	refused := &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 77), Port: 445}, Err: syscall.ECONNREFUSED}
	lookup := &net.DNSError{Err: "no such host", Name: "telescope.invalid", Server: "192.0.2.53:53", IsNotFound: true}
	for _, err := range []error{
		&os.PathError{Op: "open", Path: file, Err: os.ErrPermission},
		&os.LinkError{Op: "rename", Old: file + ".tmp", New: file, Err: syscall.EXDEV},
		fmt.Errorf("history %s: %w", file, state.ErrLocked),
		refused,
		fmt.Errorf("dial 192.0.2.77:445: %w", lookup),
		&landiscovery.PortsError{Endpoints: [2]string{"192.0.2.77:445", "192.0.2.77:4445"}, Errs: [2]error{refused, &os.PathError{Op: "mount", Path: file, Err: os.ErrPermission}}},
	} {
		if got := reason(err); got != err.Error() {
			t.Errorf("reason(%v) = %q", err, got)
		}
		e.s.copy.log.Warn("synthetic failure", "err", err)
		e.s.copy.log.Warn("synthetic failure", "error", errValuer{err})
		e.s.copy.log.Warn("synthetic failure", slog.Group("g", "reason", err))
		e.s.log.Warn("synthetic failure", "err", err)
		e.s.free.record.Warn("synthetic failure", "error", err)
	}
	lines, _ := e.s.copy.activity.lines()
	var activity strings.Builder
	for _, l := range lines {
		activity.WriteString(l.Text + "\n")
	}
	// The folder's own name, not the whole path, which a native
	// system's log quotes and escapes.
	for where, text := range map[string]string{"log": e.logs.String(), "Activity": activity.String()} {
		for _, s := range []string{"FOLDER-SENTINEL-9", "permission denied", "dial tcp 192.0.2.77:445: connection refused", "lookup telescope.invalid on 192.0.2.53:53: no such host", state.ErrLocked.Error()} {
			if !strings.Contains(text, s) {
				t.Errorf("%s lacks %q:\n%s", where, s, text)
			}
		}
	}
}

// An error with a huge text is clipped, in the log, Activity and the
// page's alert, so no record outgrows a log sink.
func TestReasonClipped(t *testing.T) {
	huge := errors.New("open " + strings.Repeat("x", 64<<10) + ": permission denied")
	if got := reason(huge); len(got) > diagnostic.MaxValue+64 || !strings.HasPrefix(got, "open xxx") || !strings.Contains(got, "…(truncated, ") {
		t.Errorf("reason = %.80q… (%d bytes)", got, len(got))
	}
	e := newAppEnv(t, nil)
	e.s.copy.log.Warn("synthetic failure", "err", huge)
	e.s.log.Warn("synthetic failure", "err", huge)
	lines, _ := e.s.copy.activity.lines()
	records := strings.Split(strings.TrimSpace(e.logs.String()), "\n")
	for _, l := range lines {
		records = append(records, l.Text)
	}
	for _, r := range records {
		if strings.Contains(r, "synthetic failure") && (len(r) >= 16<<10 || !strings.Contains(r, "…(truncated, ")) {
			t.Errorf("record of %d bytes: %.120q", len(r), r)
		}
	}
}
