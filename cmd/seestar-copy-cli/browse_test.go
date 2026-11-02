package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// lineWriter hands each write to the test.
type lineWriter chan string

func (w lineWriter) Write(p []byte) (int, error) {
	w <- string(p)
	return len(p), nil
}

// browsing is a browse run in its own goroutine.
type browsing struct {
	url    string
	stdout lineWriter
	stderr *lockedBuffer
	stop   context.CancelFunc
	code   chan int
}

// startBrowse runs browse with args after the endpoint, and waits for its
// URL line. The read-only guard is reset once the test ends.
func startBrowse(t *testing.T, args ...string) *browsing {
	t.Helper()
	t.Cleanup(readonly.Reset)
	ctx, stop := context.WithCancel(context.Background())
	b := &browsing{stdout: make(lineWriter, 4), stderr: &lockedBuffer{}, stop: stop, code: make(chan int, 1)}
	go func() {
		b.code <- run(ctx, append([]string{"browse", "--endpoint", "h:445"}, args...), b.stdout, b.stderr)
	}()
	select {
	case b.url = <-b.stdout:
	case code := <-b.code:
		t.Fatalf("exit %d before printing a URL\n%s", code, b.stderr.next())
	}
	t.Cleanup(func() { b.stop() })
	return b
}

// end stops browse and returns its exit code.
func (b *browsing) end() int {
	b.stop()
	return <-b.code
}

func (b *browsing) get(t *testing.T, route string) (int, string) {
	t.Helper()
	res, err := http.Get(strings.TrimSuffix(b.url, "\n") + route)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(body)
}

// A run prints one URL line, serves the share, stays read-only, and stops
// cleanly on a signal, closing its session.
func TestBrowseRun(t *testing.T) {
	dev := newDevice()
	dev.folder("MyWorks", findDir)
	dev.set("MyWorks/a.fit", "abc", findT0)
	dev.use(t)
	b := startBrowse(t)
	if !strings.HasPrefix(b.url, "http://127.0.0.1:") || !strings.HasSuffix(b.url, "/\n") {
		t.Fatalf("URL line %q", b.url)
	}
	code, body := b.get(t, "api/list?path=MyWorks")
	if code != 200 || !strings.Contains(body, `"path":"MyWorks/a.fit"`) || !strings.Contains(body, `"statuses":false`) {
		t.Errorf("listing = %d %s", code, body)
	}
	if code, _ := b.get(t, "file?path=MyWorks%2Fa.fit"); code != 200 {
		t.Errorf("file = %d", code)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the read-only guard is off while browse runs")
			}
		}()
		readonly.Check("test")
	}()
	if code := b.end(); code != exitOK {
		t.Errorf("exit %d", code)
	}
	stderr := b.stderr.next()
	for _, want := range []string{
		`level=INFO msg="browse listening" addr=127.0.0.1:`,
		`level=INFO msg="session opened" endpoint=h:445` + "\n",
		`level=INFO msg="session closed" endpoint=h:445 reason=stopped` + "\n",
		`level=INFO msg="browse stopped"` + "\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "unauthenticated") || strings.Contains(stderr, "dest=") {
		t.Errorf("loopback run without --dest:\n%s", stderr)
	}
	if len(b.stdout) != 0 {
		t.Errorf("more than one line on stdout: %q", <-b.stdout)
	}
	if dials, closes := dev.counts(); dials != 1 || closes != 1 {
		t.Errorf("%d dials, %d closes; want 1 and 1", dials, closes)
	}
}

// With --dest, the state is read at start, and a listing reads it again
// once the state file has changed.
func TestBrowseStatuses(t *testing.T) {
	statusDevice(t)
	dest, stateDir := filepath.Join(t.TempDir(), "dest"), filepath.Join(t.TempDir(), "state")
	writeFindState(t, dest, stateDir)
	b := startBrowse(t, "--dest", dest, "--state-dir", stateDir)
	code, body := b.get(t, "api/list?path=MyWorks")
	for _, want := range []string{`"statuses":true`, `"path":"MyWorks/a.fit","type":"file","size":3,"mtime":"2026-11-01T05:04:05.1234567Z","status":"copied"`,
		`"path":"MyWorks/d.fit","type":"file","size":3,"mtime":"2026-11-01T05:04:05.1234567Z","status":"new"`} {
		if code != 200 || !strings.Contains(body, want) {
			t.Errorf("listing = %d, lacks %s:\n%s", code, want, body)
		}
	}
	p, err := state.Path(stateDir, dest)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"path":"MyWorks/d.fit","size":3,"mtime":"2026-11-01T05:04:05.1234567Z","sha256":"` +
		strings.Repeat("0", 64) + `","at":"2026-11-01T07:00:00Z","how":"copied"}` + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	code, body = b.get(t, "api/list?path=MyWorks")
	if want := `"path":"MyWorks/d.fit","type":"file","size":3,"mtime":"2026-11-01T05:04:05.1234567Z","status":"copied"`; code != 200 || !strings.Contains(body, want) {
		t.Errorf("listing after an append = %d, lacks %s:\n%s", code, want, body)
	}
	if code, stderr := b.end(), b.stderr.next(); code != exitOK || !strings.Contains(stderr, " dest="+dest) {
		t.Errorf("exit %d\n%s", code, stderr)
	}
}

// --open gets the printed URL and browse's log; a failing opener is a
// warning and browse keeps serving.
func TestBrowseOpen(t *testing.T) {
	statusDevice(t)
	old := openBrowser
	t.Cleanup(func() { openBrowser = old })
	for _, fail := range []bool{false, true} {
		opened := make(chan string, 1)
		openBrowser = func(url string, log *slog.Logger) {
			opened <- url
			if fail {
				warnOpen(log, errors.New("no opener"))
			}
		}
		b := startBrowse(t, "--open")
		if got := <-opened; got+"\n" != b.url {
			t.Errorf("opened %q, printed %q", got, b.url)
		}
		if code, _ := b.get(t, "api/list"); code != 200 {
			t.Errorf("after --open: listing = %d", code)
		}
		if code := b.end(); code != exitOK {
			t.Errorf("exit %d", code)
		}
		stderr := b.stderr.next()
		if warned := strings.Contains(stderr, `level=WARN msg="could not open a browser" error="no opener"`); warned != fail {
			t.Errorf("failing opener %v, warned %v:\n%s", fail, warned, stderr)
		}
	}
}

// freePort returns a loopback address that was free a moment ago.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// browse cannot start: a port in use, a state that does not load, or a
// stdout that fails. Each exits 1 having dialed nothing and printed nothing,
// and leaves the port free.
func TestBrowseFails(t *testing.T) {
	dev := newDevice()
	dev.use(t)
	t.Cleanup(readonly.Reset)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	code, stdout, stderr := invoke(t, "browse", "--endpoint", "h:445", "--listen", busy.Addr().String())
	if code != exitFailure || stdout != "" || !strings.Contains(stderr, `msg="browse failed"`) || strings.Contains(stderr, "listening") {
		t.Errorf("port in use: exit %d, stdout %q\n%s", code, stdout, stderr)
	}

	dest, stateDir := filepath.Join(t.TempDir(), "dest"), filepath.Join(t.TempDir(), "state")
	p, err := state.Path(stateDir, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{\"v\":1}\nnot json\n{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	addr := freePort(t)
	code, stdout, stderr = invoke(t, "browse", "--endpoint", "h:445", "--listen", addr, "--dest", dest, "--state-dir", stateDir)
	if code != exitFailure || stdout != "" || !strings.Contains(stderr, `msg="browse failed"`) || strings.Contains(stderr, "listening") {
		t.Errorf("bad state: exit %d, stdout %q\n%s", code, stdout, stderr)
	}

	var errs bytes.Buffer
	code = run(context.Background(), []string{"browse", "--endpoint", "h:445", "--listen", addr}, &failingWriter{}, &errs)
	if code != exitFailure || !strings.Contains(errs.String(), `msg="browse failed" error="print the URL: output unavailable"`) {
		t.Errorf("failing stdout: exit %d\n%s", code, errs.String())
	}
	if ln, err := net.Listen("tcp", addr); err != nil {
		t.Errorf("the port is still in use after browse failed: %v", err)
	} else {
		ln.Close()
	}
	if dials, _ := dev.counts(); dials != 0 {
		t.Errorf("%d dials", dials)
	}
}

// Usage errors exit 2 before anything is listened on or read.
func TestBrowseUsage(t *testing.T) {
	dev := newDevice()
	dev.use(t)
	const listenRule = "give an IP address and port, such as 127.0.0.1:0"
	for _, tc := range []struct {
		args         []string
		option, rule string
	}{
		{[]string{"browse"}, "--endpoint", "required; give the SMB host:port"},
		{[]string{"browse", "--endpoint", "h"}, "--endpoint", "use host:port, such as 127.0.0.1:4445"},
		{[]string{"browse", "--endpoint", "h:445", "--listen", "localhost:0"}, "--listen", listenRule},
		{[]string{"browse", "--endpoint", "h:445", "--listen", "example.com:0"}, "--listen", listenRule},
		{[]string{"browse", "--endpoint", "h:445", "--listen", "[::ffff:127.0.0.1]:0"}, "--listen", listenRule},
		{[]string{"browse", "--endpoint", "h:445", "--listen", "[fe80::1%lo]:0"}, "--listen", listenRule},
		{[]string{"browse", "--endpoint", "h:445", "--listen", "127.0.0.1"}, "--listen", listenRule},
		{[]string{"browse", "--endpoint", "h:445", "--listen", "127.0.0.1:70000"}, "--listen", listenRule},
		{[]string{"browse", "--endpoint", "h:445", "--listen="}, "--listen", listenRule},
		{[]string{"browse", "--endpoint", "h:445", "--exclude", "x"}, "--exclude", "unknown flag"},
		{[]string{"browse", "--endpoint", "h:445", "--include", "x"}, "--include", "unknown flag"},
		{[]string{"browse", "--endpoint", "h:445", "--no-default-excludes"}, "--no-default-excludes", "unknown flag"},
		{[]string{"browse", "--endpoint", "h:445", "--dest="}, "--dest", "must not be empty; omit it to browse without a status"},
		{[]string{"browse", "--endpoint", "h:445", "--dest", "d", "--state-dir="}, "--state-dir", "must not be empty; omit it for the default"},
		{[]string{"browse", "--endpoint", "h:445", "--state-dir", "s"}, "--state-dir", "give --dest with it; browse reads a state file only for a destination"},
		{[]string{"browse", "--endpoint", "h:445", "x"}, "arguments", "positional arguments are not allowed"},
	} {
		t.Cleanup(readonly.Reset)
		code, stdout, stderr := invoke(t, tc.args...)
		want := `level=ERROR msg="invalid arguments" option=` + tc.option + ` rule=` + strconv.Quote(tc.rule) + ` help="seestar-copy-cli browse --help"` + "\n"
		if code != exitUsage || stdout != "" || !strings.HasSuffix(stderr, want) || strings.Count(stderr, "\n") != 1 {
			t.Errorf("%q: exit %d, stdout %q, stderr %q; want 2 and %q", tc.args, code, stdout, stderr, want)
		}
	}
	if dials, _ := dev.counts(); dials != 0 {
		t.Errorf("dialed %d times on usage errors", dials)
	}
	for _, ok := range []string{"127.0.0.1:0", "127.0.0.2:0", "[::1]:0", "192.168.1.5:0", "0.0.0.0:0", "[::]:0", "[2001:db8::5]:8080"} {
		if _, err := parseListen(ok); err != nil {
			t.Errorf("--listen %s: %v", ok, err)
		}
	}
}

func TestPageURL(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1:53817":   "http://127.0.0.1:53817/",
		"[::1]:53817":       "http://[::1]:53817/",
		"192.168.1.5:53817": "http://192.168.1.5:53817/",
		"0.0.0.0:53817":     "http://127.0.0.1:53817/",
		"[::]:53817":        "http://[::1]:53817/",
	} {
		if got := pageURL(netip.MustParseAddrPort(addr)); got != want {
			t.Errorf("pageURL(%s) = %s, want %s", addr, got, want)
		}
	}
}

func TestExposed(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1": false, "127.0.0.2": false, "::1": false,
		"192.168.1.5": true, "0.0.0.0": true, "::": true,
	} {
		if got := exposed(netip.MustParseAddr(addr)); got != want {
			t.Errorf("exposed(%s) = %v, want %v", addr, got, want)
		}
	}
}
