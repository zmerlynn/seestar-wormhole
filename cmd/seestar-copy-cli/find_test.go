package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// find runs find against the fake share, leaving the read-only guard off
// afterward for the other tests in this process.
func find(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Cleanup(readonly.Reset)
	return invoke(t, append([]string{"find", "--endpoint", "h:445"}, args...)...)
}

// Times for the find tests: t0 has 100 ns precision, as SMB does, and is
// in another zone, which the output turns into UTC.
var (
	findT0  = time.Date(2026, 10, 31, 22, 4, 5, 123456700, time.FixedZone("x", -7*3600))
	findDir = time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)
)

func (d *device) folder(p string, mtime time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.files[p] = &fstest.MapFile{Mode: fs.ModeDir | 0o755, ModTime: mtime}
}

// The text format: one line per folder and file in listing order, sizes in
// bytes, UTC mtimes, and paths quoted when they hold a space, a quote, a
// backslash, a character that is not printable or bytes that are not
// UTF-8. Excluded entries get a skipped line and are not listed.
func TestFindText(t *testing.T) {
	dev := newDevice()
	dev.folder("MyWorks", findDir)
	dev.folder("MyWorks/M 31", findDir)
	dev.set("MyWorks/M 31/a.fit", "abc", findT0)
	dev.set("MyWorks/b.jpg", "abcd", findDir)
	for _, p := range []string{`back\slash`, "bad\xff", "café.fit", "nb\u00a0sp", "new\nline.fit", "plain.fit", `quo"te`, "tab\tname.fit", ".DS_Store", "log/a.log"} {
		dev.set(p, "x", findDir)
	}
	dev.use(t)
	code, stdout, stderr := find(t)
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	want := `d 0 2026-10-31T01:00:00Z MyWorks
d 0 2026-10-31T01:00:00Z "MyWorks/M 31"
- 3 2026-11-01T05:04:05.1234567Z "MyWorks/M 31/a.fit"
- 4 2026-10-31T01:00:00Z MyWorks/b.jpg
- 1 2026-10-31T01:00:00Z "back\\slash"
- 1 2026-10-31T01:00:00Z "bad\xff"
- 1 2026-10-31T01:00:00Z café.fit
- 1 2026-10-31T01:00:00Z "nb\u00a0sp"
- 1 2026-10-31T01:00:00Z "new\nline.fit"
- 1 2026-10-31T01:00:00Z plain.fit
- 1 2026-10-31T01:00:00Z "quo\"te"
- 1 2026-10-31T01:00:00Z "tab\tname.fit"
`
	if stdout != want {
		t.Errorf("stdout:\n%s\nwant:\n%s", stdout, want)
	}
	for _, line := range strings.SplitAfter(strings.TrimSuffix(stdout, "\n"), "\n") {
		fields := strings.SplitN(strings.TrimSuffix(line, "\n"), " ", 4)
		if p := fields[3]; strings.HasPrefix(p, `"`) {
			if _, err := strconv.Unquote(p); err != nil {
				t.Errorf("%q does not unquote: %v", p, err)
			}
		}
	}
	for _, want := range []string{
		"level=INFO msg=skipped path=.DS_Store exclude=.*\n",
		"level=INFO msg=skipped path=log exclude=/log\n",
		`level=INFO msg="listing done" files=10 folders=2 bytes=15 recent=0 skipped=2 unmatched=0 elapsed=`,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if n := strings.Count(stderr, "\n"); n != 3 {
		t.Errorf("stderr has %d lines, want 3:\n%s", n, stderr)
	}
}

func TestQuotePath(t *testing.T) {
	for in, want := range map[string]string{
		"MyWorks/M31/a.fit": "MyWorks/M31/a.fit",
		"星/ü.fit":           "星/ü.fit",
		"a b":               `"a b"`,
		`"a`:                `"\"a"`,
		`a\b`:               `"a\\b"`,
		"a\tb":              `"a\tb"`,
		"a\nb":              `"a\nb"`,
		"a\rb":              `"a\rb"`,
		"a\x00b":            `"a\x00b"`,
		"a\x7fb":            `"a\x7fb"`,
		"a\u2028b":          `"a\u2028b"`,
		"a\u00a0b":          `"a\u00a0b"`,
		"a\xffb":            `"a\xffb"`,
		"a\ufffdb":          "a\ufffdb",
	} {
		if got := quotePath(in); got != want {
			t.Errorf("quotePath(%q) = %s, want %s", in, got, want)
		}
	}
}

// statusDevice is a share with a file of each status against the state
// that writeFindState writes, and a name that HTML escaping would change.
func statusDevice(t *testing.T) {
	dev := newDevice()
	dev.folder("MyWorks", findDir)
	dev.set("MyWorks/a.fit", "abc", findT0)  // copied
	dev.set("MyWorks/b.fit", "abcd", findT0) // changed: size
	dev.set("MyWorks/c.fit", "abc", findT0)  // changed: mtime
	dev.set("MyWorks/d.fit", "abc", findT0)  // new
	dev.set("MyWorks/<x>&.fit", "", findT0)  // new
	dev.use(t)
}

// writeFindState writes, byte for byte, a state for dest under stateDir.
func writeFindState(t *testing.T, dest, stateDir string) {
	t.Helper()
	abs, err := filepath.Abs(dest)
	if err != nil {
		t.Fatal(err)
	}
	p, err := state.Path(stateDir, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := strings.Repeat("0", 64)
	data := `{"v":1,"dest":` + strconv.Quote(abs) + `}
{"path":"MyWorks/a.fit","size":3,"mtime":"2026-11-01T05:04:05.1234567Z","sha256":"` + sum + `","at":"2026-11-01T06:00:00Z","how":"copied"}
{"path":"MyWorks/b.fit","size":3,"mtime":"2026-11-01T05:04:05.1234567Z","sha256":"` + sum + `","at":"2026-11-01T06:00:00Z","how":"copied"}
{"path":"MyWorks/c.fit","size":3,"mtime":"2026-11-01T05:04:05.1234568Z","sha256":"` + sum + `","at":"2026-11-01T06:00:00Z","how":"adopted"}
{"path":"MyWorks/gone.fit","size":3,"mtime":"2026-11-01T05:04:05Z","sha256":"` + sum + `","at":"2026-11-01T06:00:00Z","how":"copied"}
`
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// With --dest, each file gets a status from the state, in both formats,
// and neither the destination nor the state changes.
func TestFindStatuses(t *testing.T) {
	statusDevice(t)
	dest, stateDir := filepath.Join(t.TempDir(), "dest"), filepath.Join(t.TempDir(), "state")
	writeFindState(t, dest, stateDir)
	destBefore, stateBefore := snapshot(t, dest), snapshot(t, stateDir)
	for _, tc := range []struct{ format, want string }{
		{"text", `d 0 2026-10-31T01:00:00Z - MyWorks
- 0 2026-11-01T05:04:05.1234567Z new MyWorks/<x>&.fit
- 3 2026-11-01T05:04:05.1234567Z copied MyWorks/a.fit
- 4 2026-11-01T05:04:05.1234567Z changed MyWorks/b.fit
- 3 2026-11-01T05:04:05.1234567Z changed MyWorks/c.fit
- 3 2026-11-01T05:04:05.1234567Z new MyWorks/d.fit
`},
		{"jsonl", `{"path":"MyWorks","type":"dir","size":0,"mtime":"2026-10-31T01:00:00Z"}
{"path":"MyWorks/<x>&.fit","type":"file","size":0,"mtime":"2026-11-01T05:04:05.1234567Z","status":"new"}
{"path":"MyWorks/a.fit","type":"file","size":3,"mtime":"2026-11-01T05:04:05.1234567Z","status":"copied"}
{"path":"MyWorks/b.fit","type":"file","size":4,"mtime":"2026-11-01T05:04:05.1234567Z","status":"changed"}
{"path":"MyWorks/c.fit","type":"file","size":3,"mtime":"2026-11-01T05:04:05.1234567Z","status":"changed"}
{"path":"MyWorks/d.fit","type":"file","size":3,"mtime":"2026-11-01T05:04:05.1234567Z","status":"new"}
`},
	} {
		code, stdout, stderr := find(t, "--dest", dest, "--state-dir", stateDir, "--format", tc.format)
		if code != exitOK || stdout != tc.want {
			t.Errorf("%s: exit %d, stdout:\n%s\nwant:\n%s\n%s", tc.format, code, stdout, tc.want, stderr)
		}
		want := `level=INFO msg="listing done" files=5 folders=1 bytes=13 recent=0 skipped=0 unmatched=0 copied=1 changed=2 new=2 elapsed=`
		if !strings.Contains(stderr, want) {
			t.Errorf("%s: stderr lacks %q:\n%s", tc.format, want, stderr)
		}
	}
	if !maps.Equal(snapshot(t, dest), destBefore) || !maps.Equal(snapshot(t, stateDir), stateBefore) {
		t.Error("find changed the destination or the state")
	}
}

// Without --dest no state is read: a state that would fail to load is not
// touched, and there is no status column or member.
func TestFindWithoutDestReadsNoState(t *testing.T) {
	statusDevice(t)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", "")
	code, stdout, stderr := find(t, "--format", "jsonl")
	if code != exitOK || strings.Contains(stdout, "status") {
		t.Errorf("exit %d, stdout:\n%s\n%s", code, stdout, stderr)
	}
	if strings.Contains(stderr, "copied=") {
		t.Errorf("summary has status counts:\n%s", stderr)
	}
}

// A destination and state folder that do not exist: every file is new, as
// in a dry run, and neither is created.
func TestFindMissingDestination(t *testing.T) {
	statusDevice(t)
	dest, stateDir := filepath.Join(t.TempDir(), "dest"), filepath.Join(t.TempDir(), "state")
	code, stdout, stderr := find(t, "--dest", dest, "--state-dir", stateDir)
	if code != exitOK || strings.Count(stdout, " new ") != 5 || strings.Contains(stdout, " copied ") {
		t.Errorf("exit %d, stdout:\n%s\n%s", code, stdout, stderr)
	}
	for _, p := range []string{dest, stateDir} {
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s: %v, want it not created", p, err)
		}
	}
}

// A state that exists for a destination that does not: find still gives
// each file its status from the state, and creates no destination.
func TestFindMissingDestinationWithState(t *testing.T) {
	statusDevice(t)
	dest, stateDir := filepath.Join(t.TempDir(), "dest"), filepath.Join(t.TempDir(), "state")
	writeFindState(t, dest, stateDir)
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := find(t, "--dest", dest, "--state-dir", stateDir)
	want := `d 0 2026-10-31T01:00:00Z - MyWorks
- 0 2026-11-01T05:04:05.1234567Z new MyWorks/<x>&.fit
- 3 2026-11-01T05:04:05.1234567Z copied MyWorks/a.fit
- 4 2026-11-01T05:04:05.1234567Z changed MyWorks/b.fit
- 3 2026-11-01T05:04:05.1234567Z changed MyWorks/c.fit
- 3 2026-11-01T05:04:05.1234567Z new MyWorks/d.fit
`
	if code != exitOK || stdout != want {
		t.Errorf("exit %d, stdout:\n%s\nwant:\n%s\n%s", code, stdout, want, stderr)
	}
	if _, err := os.Lstat(dest); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s: %v, want it not created", dest, err)
	}
}

// find turns the read-only guard on, so the coverage test of
// internal/readonly protects it as it does a dry run.
func TestFindIsReadOnly(t *testing.T) {
	statusDevice(t)
	if code, _, stderr := find(t); code != exitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	defer func() {
		if recover() == nil {
			t.Error("the read-only guard is off after find")
		}
	}()
	readonly.Check("test")
}

// Selection is sync's: excludes skip and prune, so the unreadable
// batch_stack_tmp is never read; includes keep files in a matching folder,
// and folders they rule out are left out unread.
func TestFindSelection(t *testing.T) {
	selectionDevice(t)
	code, stdout, stderr := find(t, "--include", "/MyWorks", "--include", "/diag.png", "--exclude", "z.fit")
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	var paths []string
	for line := range strings.Lines(stdout) {
		f := strings.SplitN(strings.TrimSuffix(line, "\n"), " ", 4)
		paths = append(paths, f[0]+" "+f[3])
	}
	want := []string{`d MyWorks`, `d "MyWorks/M 31"`, `d "MyWorks/M 31/log"`, `- "MyWorks/M 31/log/x.txt"`, `- diag.png`}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Errorf("listed:\n%s\nwant:\n%s", strings.Join(paths, "\n"), strings.Join(want, "\n"))
	}
	for _, want := range []string{
		`msg=skipped path="MyWorks/M 31/Thumbs.db" exclude=Thumbs.db` + "\n",
		`msg=skipped path="MyWorks/M 31/z.fit" exclude=z.fit` + "\n",
		`msg=skipped path=MyWorks/desktop.ini exclude=desktop.ini` + "\n",
		`msg=skipped path=batch_stack_tmp exclude=/batch_stack_tmp` + "\n",
		`msg="listing done" files=2 folders=3 bytes=6 recent=0 skipped=10 unmatched=3 elapsed=`,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
}

// --older-than keeps files at least that old, counts the others as
// recent, and lists folders whatever their age.
func TestFindOlderThan(t *testing.T) {
	now := time.Now()
	dev := newDevice()
	dev.folder("MyWorks", now)
	dev.set("MyWorks/week.fit", "a", now.Add(-8*24*time.Hour))
	dev.set("MyWorks/day.fit", "a", now.Add(-2*24*time.Hour))
	dev.set("MyWorks/hour.fit", "a", now.Add(-2*time.Hour))
	dev.set("MyWorks/future.fit", "a", now.Add(time.Hour))
	dev.use(t)
	for _, tc := range []struct {
		age  string
		want []string
	}{
		{"", []string{"MyWorks", "MyWorks/day.fit", "MyWorks/future.fit", "MyWorks/hour.fit", "MyWorks/week.fit"}},
		{"0", []string{"MyWorks", "MyWorks/day.fit", "MyWorks/future.fit", "MyWorks/hour.fit", "MyWorks/week.fit"}},
		{"1h", []string{"MyWorks", "MyWorks/day.fit", "MyWorks/hour.fit", "MyWorks/week.fit"}},
		{"1d", []string{"MyWorks", "MyWorks/day.fit", "MyWorks/week.fit"}},
		{"7d", []string{"MyWorks", "MyWorks/week.fit"}},
		{"30d", []string{"MyWorks"}},
	} {
		args := []string{}
		if tc.age != "" {
			args = append(args, "--older-than", tc.age)
		}
		code, stdout, stderr := find(t, args...)
		var got []string
		for line := range strings.Lines(stdout) {
			got = append(got, strings.SplitN(strings.TrimSuffix(line, "\n"), " ", 4)[3])
		}
		if code != exitOK || strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("--older-than %q: exit %d, listed %q, want %q\n%s", tc.age, code, got, tc.want, stderr)
		}
		recent := "recent=" + strconv.Itoa(5-len(tc.want)) + " "
		if !strings.Contains(stderr, recent) {
			t.Errorf("--older-than %q: stderr lacks %q:\n%s", tc.age, recent, stderr)
		}
	}
}

// A failed connection or listing exits 1 with nothing on stdout, after the
// skipped lines found before it.
func TestFindFails(t *testing.T) {
	dev := selectionDevice(t)
	code, stdout, stderr := find(t, "--no-default-excludes", "--exclude", ".*")
	if code != exitFailure || stdout != "" {
		t.Errorf("exit %d, stdout %q, want 1 and none", code, stdout)
	}
	for _, want := range []string{
		"level=INFO msg=skipped path=.DS_Store exclude=.*\n",
		`level=ERROR msg="listing failed" files=0 folders=0 bytes=0 recent=0 skipped=2 unmatched=0 elapsed=`,
		`level=ERROR msg="find failed" error="list the share: `,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	dev.dialErrs = []error{errors.New("connection refused")}
	code, stdout, stderr = find(t)
	if code != exitFailure || stdout != "" || !strings.Contains(stderr, `msg="find failed" error="connect to h:445: connection refused"`) {
		t.Errorf("exit %d, stdout %q\n%s", code, stdout, stderr)
	}
}

// A state that cannot be loaded fails find before it connects. The state
// file has a malformed line that is not the last, which every OS reads
// alike.
func TestFindBadState(t *testing.T) {
	dev := newDevice()
	dev.use(t)
	dest, stateDir := filepath.Join(t.TempDir(), "dest"), filepath.Join(t.TempDir(), "state")
	abs, err := filepath.Abs(dest)
	if err != nil {
		t.Fatal(err)
	}
	p, err := state.Path(stateDir, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"v":1,"dest":` + strconv.Quote(abs) + `}
not json
{}
`
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := find(t, "--dest", dest, "--state-dir", stateDir)
	if dials, _ := dev.counts(); code != exitFailure || stdout != "" || dials != 0 || !strings.Contains(stderr, "line 2: invalid character") {
		t.Errorf("exit %d, stdout %q, dials %d; want 1, none, 0\n%s", code, stdout, dials, stderr)
	}
}

// A listing that cannot be written exits 1.
func TestFindWriteFails(t *testing.T) {
	statusDevice(t)
	t.Cleanup(readonly.Reset)
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"find", "--endpoint", "h:445"}, &failingWriter{}, &stderr)
	if code != exitFailure || !strings.Contains(stderr.String(), `msg="find failed" error="write the listing: output unavailable"`) {
		t.Errorf("exit %d\n%s", code, stderr.String())
	}
}

// cancelingWriter cancels the invocation on its first write, as a signal
// during the output would, and counts the writes.
type cancelingWriter struct {
	cancel context.CancelFunc
	writes int
}

func (w *cancelingWriter) Write(p []byte) (int, error) {
	w.writes++
	w.cancel()
	return len(p), nil
}

// A signal while find writes the listing exits 1 and stops the output:
// with a short listing, at the final flush; with a long one, at the next
// entry, so nothing more is written.
func TestFindStoppedDuringOutput(t *testing.T) {
	for _, n := range []int{1, 500} {
		dev := newDevice()
		for i := range n {
			dev.set(fmt.Sprintf("MyWorks/light_%04d.fit", i), "a", findT0)
		}
		dev.use(t)
		t.Cleanup(readonly.Reset)
		ctx, cancel := context.WithCancel(context.Background())
		w := &cancelingWriter{cancel: cancel}
		var stderr bytes.Buffer
		code := run(ctx, []string{"find", "--endpoint", "h:445"}, w, &stderr)
		cancel()
		if code != exitFailure || w.writes != 1 || !strings.Contains(stderr.String(), `msg="find failed" error="write the listing: context canceled"`) {
			t.Errorf("%d files: exit %d, %d writes; want 1 and 1\n%s", n, code, w.writes, stderr.String())
		}
	}
}

// Usage errors exit 2 before anything is read, with one line naming the
// option and the rule.
func TestFindUsage(t *testing.T) {
	dev := newDevice()
	dev.use(t)
	for _, tc := range []struct {
		args         []string
		option, rule string
	}{
		{[]string{"find"}, "--endpoint", "required; give the SMB host:port"},
		{[]string{"find", "--endpoint", "h"}, "--endpoint", "use host:port, such as 127.0.0.1:4445"},
		{[]string{"find", "--endpoint", "h:445", "--format", "csv"}, "--format", "use text or jsonl"},
		{[]string{"find", "--endpoint", "h:445", "--older-than", "1d12h"}, "--older-than", "use a duration such as 36h, or whole days such as 7d"},
		{[]string{"find", "--endpoint", "h:445", "--older-than", "7"}, "--older-than", "use a duration such as 36h, or whole days such as 7d"},
		{[]string{"find", "--endpoint", "h:445", "--older-than", "-1h"}, "--older-than", "must not be negative"},
		{[]string{"find", "--endpoint", "h:445", "--dest="}, "--dest", "must not be empty; omit it to list without a status"},
		{[]string{"find", "--endpoint", "h:445", "--dest", "d", "--state-dir="}, "--state-dir", "must not be empty; omit it for the default"},
		{[]string{"find", "--endpoint", "h:445", "--state-dir", "s"}, "--state-dir", "give --dest with it; find reads a state file only for a destination"},
		{[]string{"find", "--endpoint", "h:445", "--include", "a/"}, "--include", "omit the trailing /; a pattern matches files and folders"},
		{[]string{"find", "--endpoint", "h:445", "--exclude", "[a]"}, "--exclude", `[ and \ are not supported; use * and ?, and / between folders`},
		{[]string{"find", "--endpoint", "h:445", "--once"}, "--once", "unknown flag"},
		{[]string{"find", "--endpoint", "h:445", "x"}, "arguments", "positional arguments are not allowed"},
	} {
		t.Cleanup(readonly.Reset)
		code, stdout, stderr := invoke(t, tc.args...)
		want := `level=ERROR msg="invalid arguments" option=` + tc.option + ` rule=` + strconv.Quote(tc.rule) + ` help="seestar-copy-cli find --help"` + "\n"
		if code != exitUsage || stdout != "" || !strings.HasSuffix(stderr, want) || strings.Count(stderr, "\n") != 1 {
			t.Errorf("%q: exit %d, stdout %q, stderr %q; want 2 and %q", tc.args, code, stdout, stderr, want)
		}
	}
	if dials, _ := dev.counts(); dials != 0 {
		t.Errorf("dialed %d times on usage errors", dials)
	}
}
