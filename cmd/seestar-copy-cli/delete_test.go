package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// deleteOld is an mtime well past delete's default age limit, with 100 ns
// precision as SMB has.
var deleteOld = time.Now().Add(-30 * day).Truncate(time.Second).Add(1234500)

// writableShare is a share over a local folder that delete can remove
// from. Each dial, read-only or not, gets a FakeDir over the folder.
type writableShare struct {
	dir    string
	faults map[string]remote.Fault
	// last is the FakeDir of the last dial.
	last *remote.FakeDir
}

func newWritableShare(t *testing.T) *writableShare {
	return &writableShare{dir: t.TempDir()}
}

// put writes a file on the share.
func (s *writableShare) put(t *testing.T, p, data string, mtime time.Time) {
	t.Helper()
	place(t, s.dir, p, data, mtime)
}

// mkdir makes a folder on the share.
func (s *writableShare) mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(s.dir, filepath.FromSlash(p)), 0o755); err != nil {
		t.Fatal(err)
	}
}

// has reports whether p is on the share.
func (s *writableShare) has(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(s.dir, filepath.FromSlash(p)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// counts returns the last dial's remove calls and the removals they
// reached.
func (s *writableShare) counts() (calls, removes int) {
	if s.last == nil {
		return 0, 0
	}
	return s.last.Counts()
}

// use makes dial and dialRemover serve s for the rest of the test.
func (s *writableShare) use(t *testing.T) {
	oldDial, oldRemover := dial, dialRemover
	t.Cleanup(func() { dial, dialRemover = oldDial, oldRemover })
	open := func() (*remote.FakeDir, error) {
		f, err := remote.NewFakeDir(s.dir)
		if err != nil {
			return nil, err
		}
		f.Faults = s.faults
		s.last = f
		return f, nil
	}
	dial = func(context.Context, string) (remote.Session, error) { return open() }
	dialRemover = func(context.Context, string) (remote.Remover, error) { return open() }
}

// deleteRun runs delete against the fake share, leaving the read-only
// guard off afterward for the other tests in this process.
func deleteRun(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Cleanup(readonly.Reset)
	return invoke(t, append([]string{"delete", "--endpoint", "h:445"}, args...)...)
}

// stateRec is a record for writeDeleteState, of a file with data and mtime.
type stateRec struct {
	path, data string
	mtime      time.Time
}

func sha(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// recLine is a state line for r.
func recLine(r stateRec) string {
	return `{"path":` + strconv.Quote(r.path) + `,"size":` + strconv.Itoa(len(r.data)) +
		`,"mtime":"` + r.mtime.UTC().Format(time.RFC3339Nano) + `","sha256":"` + sha(r.data) +
		`","at":"2026-11-01T06:00:00Z","how":"copied"}` + "\n"
}

// writeDeleteState writes, byte for byte, a state for dest under stateDir
// holding recs and then tail, and returns its path. It does not create
// dest.
func writeDeleteState(t *testing.T, dest, stateDir, tail string, recs ...stateRec) string {
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
	data := `{"v":1,"dest":` + strconv.Quote(abs) + "}\n"
	for _, r := range recs {
		data += recLine(r)
	}
	if err := os.WriteFile(p, []byte(data+tail), 0o644); err != nil {
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

// logged returns the paths of the log lines with message msg, in order.
func logged(stderr, msg string) []string {
	re := regexp.MustCompile(`msg="?` + regexp.QuoteMeta(msg) + `"? path=("(?:[^"\\]|\\.)*"|\S+)`)
	var got []string
	for _, m := range re.FindAllStringSubmatch(stderr, -1) {
		p := m[1]
		if u, err := strconv.Unquote(p); err == nil {
			p = u
		}
		got = append(got, p)
	}
	return got
}

// modesShare is a share with a file of each kind against modesState, all
// in MyWorks/M 31:
//
//	a.fit  recorded; local copy matches
//	b.fit  recorded; local copy tampered with
//	c.fit  recorded; local copy missing
//	d.fit  not recorded
//	e.fit  recorded with another size
//	f.fit  recorded; too recent
func modesShare(t *testing.T) (*writableShare, []stateRec) {
	s := newWritableShare(t)
	recent := time.Now().Add(-time.Hour)
	recs := []stateRec{
		{"MyWorks/M 31/a.fit", "aaa", deleteOld},
		{"MyWorks/M 31/b.fit", "bbb", deleteOld},
		{"MyWorks/M 31/c.fit", "ccc", deleteOld},
		{"MyWorks/M 31/e.fit", "ee", deleteOld},
		{"MyWorks/M 31/f.fit", "fff", recent},
	}
	for _, r := range recs {
		s.put(t, r.path, r.data, r.mtime)
	}
	s.put(t, "MyWorks/M 31/e.fit", "eee", deleteOld)
	s.put(t, "MyWorks/M 31/d.fit", "ddd", deleteOld)
	return s, recs
}

// Each proof mode deletes exactly the files it proves, keeps the rest with
// a reason, and leaves the destination and the state file as they were.
func TestDeleteModes(t *testing.T) {
	all := []string{"a", "b", "c", "d", "e", "f"}
	for _, tc := range []struct {
		name    string
		args    []string
		deleted []string
		kept    []string
	}{
		{"hash", nil, []string{"a"}, []string{
			`level=WARN msg="file kept" path="MyWorks/M 31/b.fit" reason="local copy's SHA-256 differs from the state"`,
			`level=INFO msg="file kept" path="MyWorks/M 31/c.fit" reason="local copy missing; give --trust-state if it was moved"`,
			`level=INFO msg="file kept" path="MyWorks/M 31/d.fit" reason="not in the state"`,
			`level=WARN msg="file kept" path="MyWorks/M 31/e.fit" reason="changed on the device since it was copied"`,
			`msg="deletion done" deleted=1 bytes=3 folders=0 kept=4 recent=1 failed=0 skipped=0 unmatched=0 `,
		}},
		{"trust-state", []string{"--trust-state"}, []string{"a", "b", "c"}, []string{
			`msg="file kept" path="MyWorks/M 31/d.fit" reason="not in the state"`,
			`msg="file kept" path="MyWorks/M 31/e.fit" reason="changed on the device since it was copied"`,
			`msg="deletion done" deleted=3 bytes=9 folders=0 kept=2 recent=1 `,
		}},
		{"ignore-state", []string{"--ignore-state", "--include", "*.fit"}, []string{"a", "b", "c", "d", "e"}, []string{
			`msg="deletion starting" proof=none older_than=7d dry_run=false`,
			`msg="deletion done" deleted=5 bytes=15 folders=0 kept=0 recent=1 `,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, recs := modesShare(t)
			s.use(t)
			dest, stateDir := t.TempDir(), t.TempDir()
			place(t, dest, recs[0].path, recs[0].data, deleteOld)
			place(t, dest, recs[1].path, "bbx", deleteOld)
			statePath := writeDeleteState(t, dest, stateDir, "", recs...)
			stateBefore, destBefore := readFile(t, statePath), snapshot(t, dest)
			args := tc.args
			if !slices.Contains(args, "--ignore-state") {
				args = append(args, "--dest", dest, "--state-dir", stateDir)
			}
			code, stdout, stderr := deleteRun(t, append(args, "--force")...)
			if code != exitOK || stdout != "" {
				t.Fatalf("code=%d stdout=%q, want 0 and no stdout:\n%s", code, stdout, stderr)
			}
			for _, want := range tc.kept {
				if !strings.Contains(stderr, want) {
					t.Errorf("log lacks %q:\n%s", want, stderr)
				}
			}
			for _, f := range all {
				p := "MyWorks/M 31/" + f + ".fit"
				if gone := slices.Contains(tc.deleted, f); s.has(t, p) == gone {
					t.Errorf("%s: deleted %t, want %t", p, !s.has(t, p), gone)
				}
			}
			if calls, _ := s.counts(); calls != len(tc.deleted) {
				t.Errorf("%d remove calls, want %d", calls, len(tc.deleted))
			}
			if got := readFile(t, statePath); got != stateBefore {
				t.Errorf("state file changed:\n%s\nwant\n%s", got, stateBefore)
			}
			if got := snapshot(t, dest); !maps.Equal(got, destBefore) {
				t.Errorf("destination changed: %v, want %v", got, destBefore)
			}
		})
	}
}

// With --trust-state, the destination need not exist; without it, a
// missing destination is a runtime failure that deletes nothing.
func TestDeleteMissingDest(t *testing.T) {
	const noDest = "destination not found; give --trust-state if the copies were moved or the drive is offline"
	rec := stateRec{"MyWorks/a.fit", "aaa", deleteOld}
	for _, trust := range []bool{true, false} {
		s := newWritableShare(t)
		s.put(t, rec.path, rec.data, rec.mtime)
		s.use(t)
		dest, stateDir := filepath.Join(t.TempDir(), "dest"), t.TempDir()
		writeDeleteState(t, dest, stateDir, "", rec)
		args := []string{"--dest", dest, "--state-dir", stateDir}
		if trust {
			args = append(args, "--trust-state")
		}
		code, _, stderr := deleteRun(t, append(args, "--force")...)
		switch {
		case trust && (code != exitOK || s.has(t, rec.path)):
			t.Errorf("--trust-state: code=%d, file there %t; want 0 and deleted:\n%s", code, s.has(t, rec.path), stderr)
		case !trust && (code != exitFailure || !s.has(t, rec.path) || !strings.Contains(stderr, noDest)):
			t.Errorf("code=%d, file there %t; want 1, kept and %q:\n%s", code, s.has(t, rec.path), noDest, stderr)
		}
		if _, err := os.Stat(dest); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("trust %t: destination created: %v", trust, err)
		}
	}
}

func TestDeleteUsage(t *testing.T) {
	dest, stateDir := t.TempDir(), t.TempDir()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--dest", dest}, "option=--endpoint rule=\"required"},
		{[]string{"--endpoint", "h:445"}, "required; give the local mirror folder, or --ignore-state"},
		{[]string{"--endpoint", "h:445", "--dest", dest, "--trust-state", "--ignore-state"}, "give --trust-state or --ignore-state, not both"},
		{[]string{"--endpoint", "h:445", "--ignore-state"}, "give at least one --include"},
		{[]string{"--endpoint", "h:445", "--ignore-state", "--include", "*.jpg", "--dest", dest}, "option=--dest rule=\"--ignore-state reads no state; omit it"},
		{[]string{"--endpoint", "h:445", "--ignore-state", "--include", "*.jpg", "--state-dir", stateDir}, "option=--state-dir rule=\"--ignore-state reads no state; omit it"},
		{[]string{"--endpoint", "h:445", "--dest", dest, "--dry-run", "--force"}, "option=--force rule=\"give --force or --dry-run, not both"},
		{[]string{"--endpoint", "h:445", "--dest", dest, "--state-dir", ""}, "must not be empty"},
		{[]string{"--endpoint", "h:445", "--dest", dest, "--older-than", "-1h"}, "must not be negative"},
		{[]string{"--endpoint", "h:445", "--dest", dest, "--older-than", "1d12h"}, "--older-than"},
		{[]string{"--endpoint", "h:445", "--dest", dest, "--include", ""}, "--include"},
		{[]string{"--endpoint", "h:445", "--dest", dest, "extra"}, "positional arguments are not allowed"},
	} {
		s := newWritableShare(t)
		s.use(t)
		code, stdout, stderr := invoke(t, append([]string{"delete"}, tc.args...)...)
		if code != exitUsage || stdout != "" || !strings.Contains(stderr, tc.want) {
			t.Errorf("%q: code=%d stdout=%q stderr=%q, want 2 and %q", tc.args, code, stdout, stderr, tc.want)
		}
		if s.last != nil {
			t.Errorf("%q: a usage error dialed the share", tc.args)
		}
	}
}

func TestDeleteHelp(t *testing.T) {
	code, stdout, stderr := invoke(t, "delete", "--help")
	if code != exitOK || stderr != "" {
		t.Fatalf("code=%d stderr=%q, want 0 and no stderr", code, stderr)
	}
	for _, want := range []string{
		"--endpoint string", "--dest string", "--state-dir string", "--trust-state", "--ignore-state",
		"--older-than age", "(default 7d)", "--force", "--exclude stringArray", "--no-default-excludes", "--include stringArray", "--dry-run",
		"Without\n--force it only previews", "avoid running delete while the telescope is busy",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("delete help lacks %q:\n%s", want, stdout)
		}
	}
	if _, root, _ := invoke(t, "--help"); !strings.Contains(root, "  delete ") {
		t.Errorf("root help lacks delete:\n%s", root)
	}
}

// A file that changes between the listing and its removal is kept, and so
// is a folder that gets a new file.
func TestDeleteRace(t *testing.T) {
	s := newWritableShare(t)
	s.put(t, "MyWorks/a/x.fit", "x", deleteOld)
	s.put(t, "MyWorks/b/y.fit", "y", deleteOld)
	s.faults = map[string]remote.Fault{
		"MyWorks/a/x.fit": {BeforeRecheck: func() { s.put(t, "MyWorks/a/x.fit", "x2", deleteOld) }},
		"MyWorks/b":       {BeforeRecheck: func() { s.put(t, "MyWorks/b/new.fit", "n", time.Now()) }},
	}
	s.use(t)
	code, _, stderr := deleteRun(t, "--force", "--ignore-state", "--include", "*.fit")
	if code != exitOK {
		t.Fatalf("code=%d, want 0:\n%s", code, stderr)
	}
	for _, want := range []string{
		`level=WARN msg="file kept" path=MyWorks/a/x.fit reason="changed on the device during this run"`,
		`msg="file deleted" path=MyWorks/b/y.fit size=1`,
		`level=INFO msg="folder kept" path=MyWorks/b reason="not empty: something was added during the run"`,
		`msg="deletion done" deleted=1 bytes=1 folders=0 kept=1 recent=0 failed=0 `,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("log lacks %q:\n%s", want, stderr)
		}
	}
	for _, p := range []string{"MyWorks/a/x.fit", "MyWorks/b/new.fit", "MyWorks"} {
		if !s.has(t, p) {
			t.Errorf("%s is gone", p)
		}
	}
}

// A name holding a backslash, which the share would read as a folder
// separator, is never removed, nor is the file it would name, in any mode.
// TestRemoverRefusesInvalidPaths and DecideDelete's tests cover the refusal
// on every system.
func TestDeleteBackslashName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture needs a backslash in a name, which Windows reads as a separator")
	}
	saved := stateRec{"MyWorks/sub/saved.fit", "saved", deleteOld}
	for _, mode := range [][]string{{"--ignore-state"}, {"--trust-state"}, nil} {
		s := newWritableShare(t)
		s.put(t, `MyWorks/sub\saved.fit`, "other", deleteOld)
		s.put(t, saved.path, saved.data, saved.mtime)
		s.use(t)
		before := snapshot(t, s.dir)
		args := append(slices.Clone(mode), "--include", "/MyWorks/*.fit", "--older-than", "0")
		if mode == nil || mode[0] != "--ignore-state" {
			dest, stateDir := t.TempDir(), t.TempDir()
			place(t, dest, saved.path, saved.data, saved.mtime)
			writeDeleteState(t, dest, stateDir, "", saved)
			args = append(args, "--dest", dest, "--state-dir", stateDir)
		}
		code, _, stderr := deleteRun(t, append(args, "--force")...)
		if code != exitOK {
			t.Errorf("%q: code=%d, want 0:\n%s", mode, code, stderr)
		}
		if want := `level=WARN msg="file kept" path=MyWorks/sub\saved.fit reason="the share would not receive this name exactly"`; !strings.Contains(stderr, want) {
			t.Errorf("%q: log lacks %q:\n%s", mode, want, stderr)
		}
		if calls, _ := s.counts(); calls != 0 {
			t.Errorf("%q: %d remove calls, want none", mode, calls)
		}
		if after := snapshot(t, s.dir); !maps.Equal(after, before) {
			t.Errorf("%q: share changed:\n%v\nwant\n%v", mode, after, before)
		}
	}
}

// A folder with something left out of the selection stays, and a dry run
// does not report it either.
func TestDeleteKeepsFolderWithUnselected(t *testing.T) {
	for _, dry := range []bool{true, false} {
		// dry: a plain delete, which only previews.
		s := newWritableShare(t)
		s.put(t, "MyWorks/x/a.jpg", "a", deleteOld)
		s.put(t, "MyWorks/x/b.fit", "b", deleteOld)
		s.use(t)
		args := []string{"--ignore-state", "--include", "*.jpg"}
		if !dry {
			args = append(args, "--force")
		}
		code, _, stderr := deleteRun(t, args...)
		readonly.Reset()
		if code != exitOK {
			t.Errorf("dry %t: code=%d, want 0:\n%s", dry, code, stderr)
		}
		if strings.Contains(stderr, "remove folder") || strings.Contains(stderr, "folder removed") {
			t.Errorf("dry %t: a folder is removed:\n%s", dry, stderr)
		}
		if s.has(t, "MyWorks/x/a.jpg") != dry || !s.has(t, "MyWorks/x/b.fit") {
			t.Errorf("dry %t: a.jpg there %t, b.fit there %t", dry, s.has(t, "MyWorks/x/a.jpg"), s.has(t, "MyWorks/x/b.fit"))
		}
	}
}

// Emptied folders are removed, deepest first; a folder that was already
// empty stays, and so does the share root.
func TestDeleteFolders(t *testing.T) {
	s := newWritableShare(t)
	s.put(t, "top.fit", "t", deleteOld)
	s.put(t, "MyWorks/M 31/sub/a.fit", "a", deleteOld)
	s.put(t, "MyWorks/M 31/b.fit", "b", deleteOld)
	s.mkdir(t, "MyWorks/M 31/empty")
	s.put(t, "Other/c.fit", "c", deleteOld)
	s.use(t)
	code, _, stderr := deleteRun(t, "--force", "--ignore-state", "--include", "*.fit")
	if code != exitOK {
		t.Fatalf("code=%d, want 0:\n%s", code, stderr)
	}
	if got, want := logged(stderr, "folder removed"), []string{"Other", "MyWorks/M 31/sub"}; !slices.Equal(got, want) {
		t.Errorf("folders removed %q, want %q", got, want)
	}
	for p, there := range map[string]bool{"MyWorks/M 31/empty": true, "MyWorks/M 31": true, "Other": false, "top.fit": false} {
		if s.has(t, p) != there {
			t.Errorf("%s there %t, want %t", p, !there, there)
		}
	}
	if _, err := os.Stat(s.dir); err != nil {
		t.Errorf("share root: %v", err)
	}
}

// A remote failure ends the run with exit 1, and nothing after it is
// touched.
func TestDeleteRemoteFailure(t *testing.T) {
	s := newWritableShare(t)
	for _, f := range []string{"a", "b", "c"} {
		s.put(t, "MyWorks/"+f+".fit", f, deleteOld)
	}
	s.faults = map[string]remote.Fault{"MyWorks/b.fit": {BeforeRecheck: func() { _ = s.last.Fake.Close() }}}
	s.use(t)
	code, _, stderr := deleteRun(t, "--force", "--ignore-state", "--include", "*.fit")
	if code != exitFailure {
		t.Errorf("code=%d, want 1:\n%s", code, stderr)
	}
	for _, want := range []string{
		`msg="file deleted" path=MyWorks/c.fit`,
		`level=WARN msg="file failed" path=MyWorks/b.fit`,
		`level=ERROR msg="deletion failed" deleted=1 bytes=1 folders=0 kept=0 recent=0 failed=1 `,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("log lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "MyWorks/a.fit") || !s.has(t, "MyWorks/a.fit") || !s.has(t, "MyWorks/b.fit") || s.has(t, "MyWorks/c.fit") {
		t.Errorf("want only c.fit deleted and a.fit untouched:\n%s", stderr)
	}
	if calls, _ := s.counts(); calls != 2 {
		t.Errorf("%d remove calls, want 2", calls)
	}
}

// A stop mid-run exits 0, logs that the run stopped and touches nothing
// more.
func TestDeleteStopped(t *testing.T) {
	s := newWritableShare(t)
	for _, f := range []string{"a", "b", "c"} {
		s.put(t, "MyWorks/"+f+".fit", f, deleteOld)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.faults = map[string]remote.Fault{"MyWorks/b.fit": {BeforeRecheck: cancel}}
	s.use(t)
	t.Cleanup(readonly.Reset)
	var out, errs bytes.Buffer
	code := run(ctx, []string{"delete", "--endpoint", "h:445", "--force", "--ignore-state", "--include", "*.fit"}, &out, &errs)
	stderr := errs.String()
	if code != exitOK || !strings.Contains(stderr, `msg="deletion stopped" deleted=1 bytes=1 folders=0 kept=0 recent=0 failed=0 `) {
		t.Errorf("code=%d, want 0 and a stopped summary:\n%s", code, stderr)
	}
	if !s.has(t, "MyWorks/a.fit") || !s.has(t, "MyWorks/b.fit") || s.has(t, "MyWorks/c.fit") {
		t.Errorf("want only c.fit deleted:\n%s", stderr)
	}
	if calls, _ := s.counts(); calls != 2 {
		t.Errorf("%d remove calls, want 2", calls)
	}
}

// A dry run changes nothing on a writable share and reports exactly what
// a real run then does.
func TestDeleteDryRunParity(t *testing.T) {
	s, recs := modesShare(t)
	s.put(t, "MyWorks/M 42/g.fit", "ggg", deleteOld)
	recs = append(recs, stateRec{"MyWorks/M 42/g.fit", "ggg", deleteOld})
	s.use(t)
	dest, stateDir := t.TempDir(), t.TempDir()
	place(t, dest, recs[0].path, recs[0].data, deleteOld)
	place(t, dest, "MyWorks/M 42/g.fit", "ggg", deleteOld)
	statePath := writeDeleteState(t, dest, stateDir, "", recs...)
	stateBefore, destBefore, shareBefore := readFile(t, statePath), snapshot(t, dest), snapshot(t, s.dir)
	args := []string{"--dest", dest, "--state-dir", stateDir}

	code, stdout, dry := deleteRun(t, append(args, "--dry-run")...)
	readonly.Reset()
	if code != exitOK || stdout != "" {
		t.Fatalf("dry run: code=%d stdout=%q:\n%s", code, stdout, dry)
	}
	if got := snapshot(t, s.dir); !maps.Equal(got, shareBefore) {
		t.Errorf("dry run changed the share:\n%v\nwant\n%v", got, shareBefore)
	}
	if !strings.Contains(dry, `msg="dry run done" delete=2 bytes=6 remove_folders=1 kept=4 recent=1 `) {
		t.Errorf("dry run summary:\n%s", dry)
	}

	code, _, real := deleteRun(t, append(args, "--force")...)
	if code != exitOK {
		t.Fatalf("code=%d:\n%s", code, real)
	}
	if would, did := logged(dry, "would delete"), logged(real, "file deleted"); len(did) != 2 || !slices.Equal(would, did) {
		t.Errorf("dry run would delete %q, the run deleted %q", would, did)
	}
	if would, did := logged(dry, "would remove folder"), logged(real, "folder removed"); !slices.Equal(would, []string{"MyWorks/M 42"}) || !slices.Equal(would, did) {
		t.Errorf("dry run would remove %q, the run removed %q", would, did)
	}
	if would, did := logged(dry, "file kept"), logged(real, "file kept"); !slices.Equal(would, did) {
		t.Errorf("dry run would keep %q, the run kept %q", would, did)
	}
	if readFile(t, statePath) != stateBefore || !maps.Equal(snapshot(t, dest), destBefore) {
		t.Error("the state file or the destination changed")
	}
}

// The state's quirks: a file whose only record is a torn final line is
// not in the state, the last record of a path wins, a renamed local copy
// does not prove its file, nor does a copy swapped by rename for other
// bytes, and a lock held by a running sync does not stop delete, which
// takes none.
func TestDeleteStateQuirks(t *testing.T) {
	s := newWritableShare(t)
	a := stateRec{"MyWorks/a.fit", "aaa", deleteOld}
	b := stateRec{"MyWorks/b.fit", "bbb", deleteOld}
	c := stateRec{"MyWorks/c.fit", "ccc", deleteOld}
	d := stateRec{"MyWorks/d.fit", "ddd", deleteOld}
	e := stateRec{"MyWorks/e.fit", "eee", deleteOld}
	for _, r := range []stateRec{a, b, c, d, e} {
		s.put(t, r.path, r.data, r.mtime)
	}
	s.use(t)
	dest, stateDir := t.TempDir(), t.TempDir()
	for _, r := range []stateRec{a, c, d, e} {
		place(t, dest, r.path, r.data, deleteOld)
	}
	place(t, dest, "MyWorks/b renamed.fit", b.data, deleteOld)
	// e's copy is swapped, by rename, for a file of its size with other
	// bytes.
	place(t, dest, "MyWorks/e.tmp", "eex", deleteOld)
	if err := os.Rename(filepath.Join(dest, "MyWorks", "e.tmp"), filepath.Join(dest, "MyWorks", "e.fit")); err != nil {
		t.Fatal(err)
	}
	// a's record is replaced by one with another mtime; c's record with
	// another mtime is replaced by a matching one; d's only record is torn.
	stale := func(r stateRec) stateRec { r.mtime = r.mtime.Add(-time.Hour); return r }
	torn := strings.TrimSuffix(recLine(d), "\n")
	statePath := writeDeleteState(t, dest, stateDir, torn, a, stale(a), b, stale(c), c, e)
	stateBefore := readFile(t, statePath)
	// Hold the destination's own lock, as a running sync would.
	lock, err := lockState(t, stateDir, dest)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	code, _, stderr := deleteRun(t, "--force", "--dest", dest, "--state-dir", stateDir)
	if code != exitOK {
		t.Fatalf("code=%d:\n%s", code, stderr)
	}
	for _, want := range []string{
		`level=WARN msg="ignored the torn final line of the state file"`,
		`msg="file kept" path=MyWorks/a.fit reason="changed on the device since it was copied"`,
		`msg="file kept" path=MyWorks/b.fit reason="local copy missing; give --trust-state if it was moved"`,
		`msg="file deleted" path=MyWorks/c.fit size=3`,
		`msg="file kept" path=MyWorks/d.fit reason="not in the state"`,
		`level=WARN msg="file kept" path=MyWorks/e.fit reason="local copy's SHA-256 differs from the state"`,
		`msg="deletion done" deleted=1 `,
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("log lacks %q:\n%s", want, stderr)
		}
	}
	if readFile(t, statePath) != stateBefore {
		t.Error("the state file changed")
	}
}

// lockState opens the state of dest, which takes its lock, after moving
// the state file aside so that Open's repair of the torn line does not
// change the fixture; it then puts the fixture back.
func lockState(t *testing.T, stateDir, dest string) (*state.State, error) {
	t.Helper()
	p, err := state.Path(stateDir, dest)
	if err != nil {
		return nil, err
	}
	saved := readFile(t, p)
	st, err := state.Open(stateDir, dest, slog.New(slog.DiscardHandler))
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, []byte(saved), 0o644); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

// sync, then delete, then sync again copies nothing and fails nothing; a
// new file at a deleted path then replaces the old copy.
func TestDeleteBetweenSyncs(t *testing.T) {
	s := newWritableShare(t)
	s.put(t, "MyWorks/M 31/a.fit", "aaa", deleteOld)
	s.put(t, "MyWorks/M 31/b.fit", "bbb", deleteOld)
	s.use(t)
	dest, stateDir := t.TempDir(), t.TempDir()
	syncOnce := func() string {
		t.Helper()
		code, _, stderr := invoke(t, "sync", "--endpoint", "h:445", "--dest", dest, "--state-dir", stateDir, "--once")
		if code != exitOK {
			t.Fatalf("sync: code=%d:\n%s", code, stderr)
		}
		return stderr
	}
	if log := syncOnce(); !strings.Contains(log, `copied=2 `) {
		t.Fatalf("first sync:\n%s", log)
	}
	if code, _, stderr := deleteRun(t, "--force", "--dest", dest, "--state-dir", stateDir); code != exitOK ||
		!strings.Contains(stderr, `msg="deletion done" deleted=2 bytes=6 folders=2 `) {
		t.Fatalf("delete: code=%d:\n%s", code, stderr)
	}
	if s.has(t, "MyWorks") {
		t.Error("MyWorks is still on the share")
	}
	if log := syncOnce(); !strings.Contains(log, `copied=0 replaced=0 adopted=0 waiting=0 failed=0 `) {
		t.Errorf("sync after delete:\n%s", log)
	}
	checkLocal(t, dest, "MyWorks/M 31/a.fit", "aaa", deleteOld)
	newer := deleteOld.Add(time.Hour)
	s.put(t, "MyWorks/M 31/a.fit", "new", newer)
	if log := syncOnce(); !strings.Contains(log, `msg="pass done" copied=0 replaced=1 adopted=0 waiting=0 failed=0 `) {
		t.Errorf("sync of a new file:\n%s", log)
	}
	checkLocal(t, dest, "MyWorks/M 31/a.fit", "new", newer)
}

// Without --force, delete only previews: it logs the same lines as
// --dry-run, ends by naming --force, makes no remove call and changes
// nothing.
func TestDeletePreviewsWithoutForce(t *testing.T) {
	s := newWritableShare(t)
	s.put(t, "MyWorks/a/x.fit", "x", deleteOld)
	s.use(t)
	before := snapshot(t, s.dir)
	var logs []string
	for _, extra := range [][]string{nil, {"--dry-run"}} {
		code, stdout, stderr := deleteRun(t, append([]string{"--ignore-state", "--include", "*.fit"}, extra...)...)
		readonly.Reset()
		if code != exitOK || stdout != "" {
			t.Errorf("%q: code=%d stdout=%q, want 0:\n%s", extra, code, stdout, stderr)
		}
		for _, want := range []string{
			`msg="would delete" path=MyWorks/a/x.fit size=1`,
			`msg="would remove folder" path=MyWorks/a`,
			`msg="dry run done" delete=1 bytes=1 remove_folders=2 `,
		} {
			if !strings.Contains(stderr, want) {
				t.Errorf("%q: log lacks %q:\n%s", extra, want, stderr)
			}
		}
		if !strings.HasSuffix(stderr, `level=INFO msg="preview only; add --force to delete"`+"\n") {
			t.Errorf("%q: log does not end by naming --force:\n%s", extra, stderr)
		}
		if calls, _ := s.counts(); calls != 0 {
			t.Errorf("%q: %d remove calls, want none", extra, calls)
		}
		logs = append(logs, regexp.MustCompile(`time=\S+ |elapsed=\S+`).ReplaceAllString(stderr, ""))
	}
	if logs[0] != logs[1] {
		t.Errorf("plain delete logged\n%s\n--dry-run logged\n%s", logs[0], logs[1])
	}
	if after := snapshot(t, s.dir); !maps.Equal(after, before) {
		t.Errorf("share changed:\n%v\nwant\n%v", after, before)
	}
}
