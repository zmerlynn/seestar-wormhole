package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hirochachacha/go-smb2/v2/x/protocol"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/engine"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

const (
	addressError  = "Enter the telescope address, with an optional port."
	folderError   = "Enter the full path of a folder."
	historyError  = "Use a full path to a folder outside the folder for your images, or leave it empty for the default."
	intervalError = "Enter a whole number of minutes from 1 to 1440."
	filesError    = "Enter a whole number from 1 to 4."
)

// statusBadNetworkName is the NTSTATUS a server answers when it has no
// share by the name asked for.
const statusBadNetworkName = 0xC00000CC

// normalizeAddress checks an address as typed: host or host:port, where a
// bare host gets port 445 and an IPv6 host may be bare or in brackets. It
// returns host:port.
func normalizeAddress(s string) (string, bool) {
	s = strings.TrimSpace(s)
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		host, port = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"), "445"
		if strings.Contains(host, ":") && net.ParseIP(host) == nil {
			return "", false
		}
	}
	if host == "" || strings.ContainsAny(host, " \t/\\[]@?#") {
		return "", false
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return "", false
	}
	return net.JoinHostPort(host, port), true
}

// checkInput checks the form's fields and returns the settings they make,
// or a message for each field in error, keyed by field.
func checkInput(in input) (Settings, map[string]string) {
	errs := map[string]string{}
	set := Settings{Version: SettingsVersion, Folder: strings.TrimSpace(in.Folder), SkipBuiltIn: in.SkipBuiltIn, StartCopying: in.StartCopying, Once: in.Once}
	if !filepath.IsAbs(set.Folder) {
		errs["folder"] = folderError
	}
	// The history folder is checked against the folder by the strings, as
	// state.Path does; state.Open checks it again, resolved, at each use.
	// Unlike --history-dir, it can't be relative: the app has no working
	// folder to resolve it against.
	if set.HistoryDir = strings.TrimSpace(in.HistoryDir); set.HistoryDir != "" {
		if !filepath.IsAbs(set.HistoryDir) {
			errs["history_dir"] = historyError
		} else if _, err := state.Path(set.HistoryDir, set.Folder); err != nil && filepath.IsAbs(set.Folder) {
			errs["history_dir"] = historyError
		}
	}
	// Copy once never waits between checks: its interval, kept for a later
	// return to repeating, is the default when it isn't one.
	n, err := strconv.Atoi(strings.TrimSpace(in.Interval))
	switch {
	case err == nil && n >= 1 && n <= 1440:
	case in.Once:
		n = DefaultSettings().IntervalMinutes
	default:
		errs["interval"] = intervalError
	}
	set.IntervalMinutes = n
	n, err = strconv.Atoi(strings.TrimSpace(in.FilesAtOnce))
	if err != nil || n < 1 || n > MaxFilesAtOnce {
		errs["files_at_once"] = filesError
	}
	set.FilesAtOnce = n
	for _, box := range []struct {
		field, negated string
		lines          []string
	}{
		{"excludes", "excludes always win, so put the files to keep in Only copy", in.Excludes},
		{"includes", "put the files to leave out in Leave out", in.Includes},
	} {
		field := box.field
		patterns, msg := checkPatterns(box.lines, "save", box.negated)
		if msg != "" {
			errs[field] = msg
		}
		if field == "excludes" {
			set.Excludes = patterns
		} else {
			set.Includes = patterns
		}
	}
	return set, errs
}

// checkPatterns checks a pattern box's lines with the CLI's parser. Blank
// lines are skipped but counted, so the line number in a message is the
// one the user sees. It returns the patterns, or a message for the first
// line in error, which says to correct it and then press the button verb
// names. A leading ! is followed by negated, which names the other box.
func checkPatterns(lines []string, verb, negated string) ([]string, string) {
	patterns := []string{}
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if _, err := engine.ParsePattern(line); errors.Is(err, engine.ErrNegated) {
			return nil, fmt.Sprintf("Line %d: %s; %s. Correct this pattern and %s again.", i+1, err, negated, verb)
		} else if err != nil {
			return nil, fmt.Sprintf("Line %d: %s. Correct this pattern and %s again.", i+1, err, verb)
		}
		patterns = append(patterns, line)
	}
	return patterns, ""
}

// hostOS is the GOOS whose wording the page gets; tests replace it.
var hostOS = runtime.GOOS

// LocalNetworkHint is added on goos to err, a failure to reach the
// telescope at an address on this network, when nothing answered: macOS may
// be blocking Local Network access, which the failure itself can't tell.
// It is "" elsewhere, and for any failure where something answered.
func LocalNetworkHint(goos string, err error) string {
	if goos != "darwin" || !unanswered(err) {
		return ""
	}
	return " On macOS, if Local Network access is blocked, enable seestar-wormhole in System Settings > Privacy & Security > Local Network, then Test again. If a permission request is open, allow access first."
}

// unanswered reports whether err is a TCP connect that nothing answered:
// it timed out, the context's deadline passed while it waited, or the
// system failed it at once ([connectBlocked]). Apple documents no errno for
// a Local Network denial, so these are the failures where one could hide.
// A refusal, a name not found, a DNS server's answer and anything the SMB
// server said after connecting are answers.
func unanswered(err error) bool {
	op, ok := errors.AsType[*net.OpError](err)
	return ok && op.Op == "dial" && op.Timeout() || connectBlocked(err)
}

// connectBlocked reports whether err is a TCP connect the system failed
// without an answer or a timeout: there was no route to the host or
// network, the on-link host did not answer (EHOSTDOWN, which macOS returns
// for that), or the system refused to send it. While macOS asks whether
// the app may use the Local Network, its connects fail this way.
func connectBlocked(err error) bool {
	op, ok := errors.AsType[*net.OpError](err)
	if !ok || op.Op != "dial" {
		return false
	}
	sys, ok := errors.AsType[*os.SyscallError](op.Err)
	return ok && sys.Syscall == "connect" && (errors.Is(sys.Err, syscall.EHOSTUNREACH) || errors.Is(sys.Err, syscall.ENETUNREACH) || errors.Is(sys.Err, syscall.EHOSTDOWN) || errors.Is(sys.Err, syscall.EPERM))
}

// localNetworkRetry is how a dial to an address on this network waits out
// macOS's Local Network prompt; tests replace it.
var localNetworkRetry = &lanRetry{goos: runtime.GOOS, every: time.Second, limit: 5 * time.Second, now: time.Now, after: time.After}

// lanRetry is when [retryLAN] tries again: on goos "darwin" only, every
// interval, until limit has passed, and only in the first dial to an
// address on this network since the app started. Only that dial can raise
// macOS's prompt; every later one, even while the first still waits, dials
// once. now and after are the clock.
type lanRetry struct {
	goos         string
	every, limit time.Duration
	now          func() time.Time
	after        func(time.Duration) <-chan time.Time

	claimed atomic.Bool // the first dial has begun
}

// retryLAN runs try, a dial to an address on this network, and on macOS
// runs it again while it fails with [connectBlocked]: the system fails
// connects that way while its Local Network prompt is open, and the first
// one after the user allows access goes through. Only the app's first such
// dial does, for at most r's limit in all: a try again is bounded by the
// time left. Once ctx is done it returns too. It returns the last try's
// result, or the last failure before the limit cut a try short. Any other
// failure, a timeout, a refusal or anything the server said, returns at
// once.
func retryLAN[T any](ctx context.Context, r *lanRetry, try func(context.Context) (T, error)) (T, error) {
	// The first dial claims the retries for good, however it ends.
	if !r.claimed.CompareAndSwap(false, true) {
		return try(ctx)
	}
	cutoff := r.now().Add(r.limit)
	v, err := try(ctx)
	for ctx.Err() == nil && r.goos == "darwin" && connectBlocked(err) && r.now().Add(r.every).Before(cutoff) {
		select {
		case <-ctx.Done():
			return v, err
		case <-r.after(r.every):
		}
		// A timer that fired late leaves less, or nothing.
		d := cutoff.Sub(r.now())
		if d <= 0 || ctx.Err() != nil {
			return v, err
		}
		next, nextErr, cut := tryWithin(ctx, d, try)
		if cut && ctx.Err() == nil {
			var zero T
			return zero, err
		}
		v, err = next, nextErr
	}
	return v, err
}

// tryWithin runs try with a context that ends after d unless try has
// returned by then, so a session it dials keeps ctx's lifetime. cut
// reports that d ran out first: a session try dialed is then closed.
func tryWithin[T any](ctx context.Context, d time.Duration, try func(context.Context) (T, error)) (v T, err error, cut bool) {
	tctx, cancel := context.WithCancel(ctx)
	var mu sync.Mutex
	returned := false
	timer := time.AfterFunc(d, func() {
		mu.Lock()
		defer mu.Unlock()
		if !returned {
			cut = true
			cancel()
		}
	})
	v, err = try(tctx)
	timer.Stop()
	mu.Lock()
	returned = true
	mu.Unlock()
	if err != nil || cut {
		cancel()
	}
	return v, err, cut
}

// establish runs try, which opens a session at t. On this network it is
// [retryLAN]'s, so every tab's first dial waits out macOS's Local Network
// prompt; through the account it tries once.
func establish[T any](ctx context.Context, t Target, try func(context.Context) (T, error)) (T, error) {
	if t.onLAN() {
		return retryLAN(ctx, localNetworkRetry, try)
	}
	return try(ctx)
}

// hinted is a failed dial with [LocalNetworkHint] after its message, for a
// page that shows only the message. It unwraps to the dial's error.
type hinted struct {
	err  error
	hint string
}

func (e *hinted) Error() string { return strings.TrimRight(e.err.Error(), ".") + "." + e.hint }
func (e *hinted) Unwrap() error { return e.err }

// withHint adds [LocalNetworkHint] to err, a failed dial to an address on
// this network, where it applies.
func withHint(err error) error {
	if hint := LocalNetworkHint(hostOS, err); hint != "" {
		return &hinted{err, hint}
	}
	return err
}

// testMessage words Test's result for the page on goos. Test checks an
// address on this network.
func testMessage(goos, endpoint string, entries []remote.Entry, err error) string {
	host, port, _ := net.SplitHostPort(endpoint)
	var dnsErr *net.DNSError
	switch {
	case err == nil:
		var folders []string
		for _, e := range entries {
			if e.Kind == remote.Dir {
				folders = append(folders, path.Base(e.Path))
			}
		}
		const start = "Connected. The telescope's share has "
		switch n := len(folders); n {
		case 0:
			return start + "no folders."
		case 1:
			return start + "the folder " + folders[0] + "."
		case 2:
			return start + "the folders " + folders[0] + " and " + folders[1] + "."
		case 3:
			return start + folders[0] + ", " + folders[1] + " and 1 more folder."
		default:
			return fmt.Sprintf("%s%s, %s and %d more folders.", start, folders[0], folders[1], n-2)
		}
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return "Can't find " + host + ". Try the telescope's IP address."
	case errors.Is(err, syscall.ECONNREFUSED):
		return host + " answered but refused port " + port + ". Is this the telescope's address?"
	case isNoShare(err):
		return `Connected, but this isn't a Seestar: it has no "EMMC Images" share.`
	default:
		return "Can't reach " + endpoint + ". " + checkReach(true) + LocalNetworkHint(goos, err)
	}
}

// isNoShare reports whether err is a server's answer that it has no such
// share.
func isNoShare(err error) bool {
	rerr, ok := errors.AsType[*protocol.ResponseError](err)
	return ok && rerr.Code == statusBadNetworkName
}
