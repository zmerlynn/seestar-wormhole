package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// Literal fixtures. The records are written by hand, not by the encoder.
const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	shaC = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	lineM31Copied   = `{"path":"MyWorks/M 31/Light_1.fit","size":16594560,"mtime":"2026-10-29T03:31:19Z","sha256":"` + shaA + `","at":"2026-11-02T01:02:03Z","how":"copied"}` + "\n"
	lineM31Replaced = `{"path":"MyWorks/M 31/Light_1.fit","size":16594561,"mtime":"2026-10-30T03:31:19.5Z","sha256":"` + shaB + `","at":"2026-11-03T01:02:03Z","how":"replaced"}` + "\n"
	lineM31Adopted  = `{"path":"MyWorks/M 31/Light_1.fit","size":16594562,"mtime":"2026-10-31T03:31:19.000000001Z","sha256":"` + shaC + `","at":"2026-11-04T01:02:03Z","how":"adopted"}` + "\n"
	lineDark        = `{"path":"Autorun/Dark/d.fit","size":0,"mtime":"2026-10-29T01:00:00Z","sha256":"` + shaA + `","at":"2026-11-02T01:02:03Z","how":"copied"}` + "\n"
)

// headerLine is the header for dest, written by hand; only the string is
// quoted by the encoder, since temporary directories differ by platform.
func headerLine(dest string) string {
	q, err := json.Marshal(dest)
	if err != nil {
		panic(err)
	}
	return `{"v":1,"dest":` + string(q) + "}\n"
}

// dirs returns a state directory and an absolute destination for one test.
func dirs(t *testing.T) (stateDir, dest string) {
	t.Helper()
	root := t.TempDir()
	return filepath.Join(root, "state"), filepath.Join(root, "dest")
}

// writeState puts content at the state path of dest.
func writeState(t *testing.T, stateDir, dest, content string) string {
	t.Helper()
	p, err := Path(stateDir, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// logBuffer returns a logger at debug level and the text it writes.
func logBuffer() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})), &b
}

func mustOpen(t *testing.T, stateDir, dest string, log *slog.Logger) *State {
	t.Helper()
	s, err := Open(stateDir, dest, log)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestOpenCreatesHeader(t *testing.T) {
	for _, existing := range []bool{false, true} {
		stateDir, dest := dirs(t)
		if existing {
			// An empty file counts as new.
			writeState(t, stateDir, dest, "")
		}
		s := mustOpen(t, stateDir, dest, nil)
		p, _ := Path(stateDir, dest)
		if got, want := readFile(t, p), headerLine(dest); got != want {
			t.Errorf("existing=%v: state file = %q, want %q", existing, got, want)
		}
		if n := len(s.Snapshot()); n != 0 {
			t.Errorf("existing=%v: %d records, want 0", existing, n)
		}
		base := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if got, want := syncDirEntries(t, stateDir), []string{base + ".jsonl", base + ".lock", registryName}; !slices.Equal(got, want) {
			t.Errorf("existing=%v: sync directory holds %q, want %q", existing, got, want)
		}
		if runtime.GOOS != "windows" {
			for path, want := range map[string]fs.FileMode{filepath.Dir(p): 0o700 | fs.ModeDir, p: 0o600} {
				if fi, err := os.Stat(path); err != nil || fi.Mode() != want {
					t.Errorf("existing=%v: mode of %s = %v (%v), want %v", existing, path, fi.Mode(), err, want)
				}
			}
		}
	}
}

func TestOpenRefusesHeader(t *testing.T) {
	withV := func(old, new string) func(string) string {
		return func(d string) string { return strings.Replace(headerLine(d), old, new, 1) + lineDark }
	}
	for _, tc := range []struct {
		name    string
		content func(dest string) string
		want    string
	}{
		{"other dest", func(string) string { return headerLine("/elsewhere") + lineDark }, `the state is for destination "/elsewhere"`},
		{"version 2", withV(`"v":1`, `"v":2`), "line 1: state version 2 is not supported; this release reads version 1"},
		{"version -1", withV(`"v":1`, `"v":-1`), "line 1: state version -1 is not supported; this release reads version 1"},
		{"version 0", withV(`"v":1`, `"v":0`), "line 1: not a state header: no version"},
		{"no version", withV(`"v":1,`, ``), "line 1: not a state header: no version"},
		{"version string", withV(`"v":1`, `"v":"1"`), "line 1: not a state header"},
		{"record first", func(string) string { return lineDark + lineDark }, "line 1: not a state header: no version"},
		{"garbled", func(string) string { return "{\"v\":1,\"de\x00\n" + lineDark }, "line 1: not a state header: jsontext: invalid character"},
		{"blank first line", func(d string) string { return "\n" + headerLine(d) }, "line 1: not a state header"},
		{"header without newline", func(d string) string { return strings.TrimSuffix(headerLine(d), "\n") }, "line 1: the header has no newline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir, dest := dirs(t)
			content := tc.content(dest)
			p := writeState(t, stateDir, dest, content)
			s, err := Open(stateDir, dest, nil)
			if err == nil {
				s.Close()
				t.Fatalf("Open succeeded, want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), p) {
				t.Errorf("Open error = %q, want it to contain %q and the file name", err, tc.want)
			}
			if got := readFile(t, p); got != content {
				t.Errorf("refused file changed to %q, want %q", got, content)
			}
			// The refusal released the lock.
			if _, err := Open(stateDir, dest, nil); errors.Is(err, ErrLocked) {
				t.Errorf("second Open after a refusal: %v, want the same refusal", err)
			}
		})
	}
}

func TestOpenLastRecordWins(t *testing.T) {
	stateDir, dest := dirs(t)
	content := headerLine(dest) + lineM31Copied + lineDark + lineM31Replaced + lineM31Adopted
	p := writeState(t, stateDir, dest, content)
	s := mustOpen(t, stateDir, dest, nil)
	want := map[string]Record{
		"MyWorks/M 31/Light_1.fit": {
			Path: "MyWorks/M 31/Light_1.fit", Size: 16594562, MTime: mustTime(t, "2026-10-31T03:31:19.000000001Z"),
			SHA256: shaC, At: mustTime(t, "2026-11-04T01:02:03Z"), How: Adopted,
		},
		"Autorun/Dark/d.fit": {
			Path: "Autorun/Dark/d.fit", Size: 0, MTime: mustTime(t, "2026-10-29T01:00:00Z"),
			SHA256: shaA, At: mustTime(t, "2026-11-02T01:02:03Z"), How: Copied,
		},
	}
	checkRecords(t, s.Snapshot(), want)
	if rec, ok := s.Lookup("MyWorks/M 31/Light_1.fit"); !ok || rec.How != Adopted || rec.Size != 16594562 {
		t.Errorf("Lookup = %+v, %v, want the adopted record", rec, ok)
	}
	if _, ok := s.Lookup("MyWorks/M 31/other.fit"); ok {
		t.Error("Lookup of a path not in the state reported a record")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, p); got != content {
		t.Errorf("loading changed the file to %q", got)
	}
}

func TestOpenSnapshotIsACopy(t *testing.T) {
	stateDir, dest := dirs(t)
	writeState(t, stateDir, dest, headerLine(dest)+lineDark)
	s := mustOpen(t, stateDir, dest, nil)
	snap := s.Snapshot()
	delete(snap, "Autorun/Dark/d.fit")
	if _, ok := s.Lookup("Autorun/Dark/d.fit"); !ok {
		t.Error("changing a snapshot changed the state")
	}
}

func TestOpenPermissive(t *testing.T) {
	stateDir, dest := dirs(t)
	hdr := strings.Replace(headerLine(dest), `{"v":1,`, `{"v":1,"writer":"later release",`, 1)
	content := hdr +
		// Unknown members, another member order and an offset time.
		`{"how":"copied","extra":{"nested":[1,2]},"path":"a/b.fit","size":5,"mtime":"2026-10-29T05:31:19.25+02:00","sha256":"` + shaA + `","at":"2026-11-02T01:02:03Z","checked":true}` + "\n" +
		// An unknown how and no at or sha256: only path, size and mtime are needed.
		`{"path":"a/c.fit","size":6,"mtime":"2026-10-29T03:31:19Z","how":"verified"}` + "\n"
	writeState(t, stateDir, dest, content)
	log, logs := logBuffer()
	s := mustOpen(t, stateDir, dest, log)
	want := map[string]Record{
		"a/b.fit": {Path: "a/b.fit", Size: 5, MTime: mustTime(t, "2026-10-29T03:31:19.25Z"), SHA256: shaA, At: mustTime(t, "2026-11-02T01:02:03Z"), How: Copied},
		"a/c.fit": {Path: "a/c.fit", Size: 6, MTime: mustTime(t, "2026-10-29T03:31:19Z"), How: "verified"},
	}
	checkRecords(t, s.Snapshot(), want)
	if got := logs.String(); !strings.Contains(got, "level=DEBUG") || !strings.Contains(got, "how=verified") || strings.Contains(got, "level=WARN") {
		t.Errorf("log = %q, want one debug line for the unknown how and no warning", got)
	}
}

func TestOpenDropsTornFinalLine(t *testing.T) {
	for _, tc := range []struct{ name, torn, reason string }{
		{"no newline", `{"path":"MyWorks/M 31/Light_2.fit","si`, "no newline"},
		{"complete record without newline", strings.TrimSuffix(lineDark, "\n"), "no newline"},
		{"garbled with newline", "{\"path\":\"x\x00\n", "invalid character"},
		{"invalid record", `{"path":"../x","size":1,"mtime":"2026-10-29T03:31:19Z"}` + "\n", "invalid record"},
		{"blank", "\n", "jsontext: unexpected EOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir, dest := dirs(t)
			good := headerLine(dest) + lineM31Copied
			p := writeState(t, stateDir, dest, good+tc.torn)
			log, logs := logBuffer()
			s := mustOpen(t, stateDir, dest, log)
			if got := logs.String(); !strings.Contains(got, "level=WARN") || !strings.Contains(got, "torn final line") ||
				!strings.Contains(got, "line=3") || !strings.Contains(got, tc.reason) {
				t.Errorf("log = %q, want a warning naming line 3 and %q", got, tc.reason)
			}
			if got := readFile(t, p); got != good {
				t.Errorf("after Open the file is %q, want it truncated to %q", got, good)
			}
			checkRecords(t, s.Snapshot(), map[string]Record{"MyWorks/M 31/Light_1.fit": m31Copied(t)})
		})
	}
}

func TestOpenRefusesBadLine(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"garbled middle", lineM31Copied + "{nope\n" + lineDark, "line 3: jsontext: invalid character"},
		{"blank middle", lineM31Copied + "\n" + lineDark, "line 3: jsontext: unexpected EOF"},
		{"invalid middle record", `{"path":"/abs","size":1,"mtime":"2026-10-29T03:31:19Z"}` + "\n" + lineDark, `line 2: invalid record: path "/abs"`},
		{"negative size", `{"path":"a","size":-1,"mtime":"2026-10-29T03:31:19Z"}` + "\n" + lineDark, "line 2: invalid record: size -1 is negative"},
		{"no mtime", `{"path":"a","size":1}` + "\n" + lineDark, "line 2: invalid record: mtime is missing"},
		{"bad line then torn tail", "{nope\n" + `{"path":"a","si`, "line 2: jsontext: invalid character"},
		{"overlong line", strings.Repeat("x", maxLine) + "\n", "line 2 is longer than 1048576 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir, dest := dirs(t)
			content := headerLine(dest) + tc.body
			p := writeState(t, stateDir, dest, content)
			log, logs := logBuffer()
			s, err := Open(stateDir, dest, log)
			if err == nil {
				s.Close()
				t.Fatalf("Open succeeded, want an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Open error = %q, want it to contain %q", err, tc.want)
			}
			if got := readFile(t, p); got != content {
				t.Errorf("refused file changed; got %d bytes, want %d", len(got), len(content))
			}
			if strings.Contains(logs.String(), "WARN") {
				t.Errorf("a fatal line also logged a warning: %q", logs.String())
			}
		})
	}
}

func TestOpenLongestLine(t *testing.T) {
	stateDir, dest := dirs(t)
	// A record line of exactly maxLine bytes, newline included, loads.
	prefix, suffix := `{"path":"`, `","size":1,"mtime":"2026-10-29T03:31:19Z"}`+"\n"
	path := strings.Repeat("p", maxLine-len(prefix)-len(suffix))
	line := prefix + path + suffix
	if len(line) != maxLine {
		t.Fatalf("fixture is %d bytes, want %d", len(line), maxLine)
	}
	writeState(t, stateDir, dest, headerLine(dest)+line)
	s := mustOpen(t, stateDir, dest, nil)
	if _, ok := s.Lookup(path); !ok {
		t.Error("the longest allowed line did not load")
	}
}

func TestOpenLocked(t *testing.T) {
	stateDir, dest := dirs(t)
	first := mustOpen(t, stateDir, dest, nil)
	for _, form := range []string{dest, dest + string(filepath.Separator)} {
		s, err := Open(stateDir, form, nil)
		if err == nil {
			s.Close()
			t.Fatalf("second Open(%q) succeeded while the first is open", form)
		}
		if !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), dest) {
			t.Errorf("second Open(%q) error = %q, want ErrLocked naming %s", form, err, dest)
		}
	}
	// Another destination in the same state directory is independent.
	other := mustOpen(t, stateDir, dest+"2", nil)
	other.Close()

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Errorf("second Close: %v, want nil", err)
	}
	mustOpen(t, stateDir, dest, nil)
}

func m31Copied(t *testing.T) Record {
	return Record{
		Path: "MyWorks/M 31/Light_1.fit", Size: 16594560, MTime: mustTime(t, "2026-10-29T03:31:19Z"),
		SHA256: shaA, At: mustTime(t, "2026-11-02T01:02:03Z"), How: Copied,
	}
}

// checkRecords compares records, with times compared by Equal.
func checkRecords(t *testing.T, got, want map[string]Record) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("got %d records %+v, want %d %+v", len(got), got, len(want), want)
	}
	for p, w := range want {
		g, ok := got[p]
		if !ok {
			t.Errorf("no record for %q, want %+v", p, w)
			continue
		}
		if g.Path != w.Path || g.Size != w.Size || !g.MTime.Equal(w.MTime) || g.SHA256 != w.SHA256 || !g.At.Equal(w.At) || g.How != w.How {
			t.Errorf("record for %q = %+v, want %+v", p, g, w)
		}
	}
}

func TestOpenLockErrorNamesLockAndDest(t *testing.T) {
	stateDir, dest := dirs(t)
	p, _ := Path(stateDir, dest)
	lockPath := strings.TrimSuffix(p, ".jsonl") + ".lock"
	// A directory in the lock file's place fails to open, even for root.
	if err := os.MkdirAll(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(stateDir, dest, nil)
	if err == nil {
		s.Close()
		t.Fatal("Open succeeded with a directory in place of the lock file")
	}
	if errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "lock "+lockPath+" for destination "+dest+": ") {
		t.Errorf("Open error = %q, want a non-ErrLocked error naming %s and %s", err, lockPath, dest)
	}
	var pe *fs.PathError
	if !errors.As(err, &pe) {
		t.Errorf("Open error = %q does not wrap the underlying *fs.PathError", err)
	}
}
