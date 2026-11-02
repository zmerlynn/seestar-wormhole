package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

func TestExcludeMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		match   []string
		miss    []string
	}{
		// No slash: the last name, at any depth, file or folder.
		{".*", []string{".DS_Store", ".StackReplayTemp", "MyWorks/M 31/._a.fit", "a/.b/c/.d"}, []string{"MyWorks/a.fit", "a.b", "MyWorks/x.y"}},
		{"*.tmp", []string{"x.mp4.tmp", "Video/x.mp4.TMP", "a.tmp"}, []string{"tmp", "a.tmp/b", "a.tmpx"}},
		{"Thumbs.db", []string{"Thumbs.db", "MyWorks/M 31/thumbs.DB"}, []string{"Thumbs.db.bak", "xThumbs.db"}},
		{"$RECYCLE.BIN", []string{"$Recycle.Bin"}, []string{"RECYCLE.BIN"}},
		{"System Volume Information", []string{"system volume information"}, []string{"System  Volume Information", "SystemVolumeInformation"}},
		{"M?", []string{"M1", "a/M1", "m1", "M星"}, []string{"M12", "M", "M1/x"}},
		// ? and * never cross a slash.
		{"Autorun?Dark", nil, []string{"Autorun/Dark", "x/Autorun/Dark"}},
		{"Autorun*", []string{"Autorun", "AutorunX"}, []string{"Autorun/Dark"}},
		// A leading slash anchors at the share root.
		{"/log", []string{"log", "LOG", "Log"}, []string{"MyWorks/M 31/log", "logs", "log/x", "a/log"}},
		// A slash in the middle anchors too, with or without the leading one.
		{"MyWorks/M 31", []string{"MyWorks/M 31", "myworks/m 31"}, []string{"a/MyWorks/M 31", "M 31", "MyWorks/M 31/x", "MyWorks"}},
		{"/MyWorks/M 31", []string{"MyWorks/M 31"}, []string{"a/MyWorks/M 31", "M 31"}},
		{"MyWorks/*", []string{"MyWorks/M 31", "MyWorks/a.fit"}, []string{"MyWorks", "MyWorks/M 31/a.fit", "x/MyWorks/a"}},
		{"*/Dark", []string{"Autorun/Dark"}, []string{"Dark", "x/Autorun/Dark"}},
		{"/Autorun/?ark", []string{"Autorun/Dark", "autorun/dark"}, []string{"Autorun/Dark/x", "Autorun/ark"}},
		// Case is ignored by lowercasing both sides, with no Unicode
		// normalization or full case folding.
		{"Café.FIT", []string{"CAFÉ.fit"}, []string{"Cafe\u0301.fit"}},
		{"ς", nil, []string{"Σ"}},
	} {
		e, err := parseExclude(tc.pattern)
		if err != nil {
			t.Errorf("%q: %v", tc.pattern, err)
			continue
		}
		if e.flag != tc.pattern {
			t.Errorf("%q: flag %q, want it as given", tc.pattern, e.flag)
		}
		for _, p := range tc.match {
			if !e.match(p) {
				t.Errorf("%q does not match %q", tc.pattern, p)
			}
		}
		for _, p := range tc.miss {
			if e.match(p) {
				t.Errorf("%q matches %q", tc.pattern, p)
			}
		}
	}
}

func TestExcludeParseErrors(t *testing.T) {
	for _, p := range []string{
		"", "/", ".", "..", "./a", "a/.", "a/..", "/../log", "a//b", "//x",
		"log/", "/log/", "//",
		"M[42]", "[", `MyWorks\x`, `\`,
		"**", "a**", "MyWorks/**/x",
	} {
		if e, err := parseExclude(p); err == nil {
			t.Errorf("%q: accepted as %+v", p, e)
		}
	}
	// Spaces are kept, not trimmed.
	for _, p := range []string{" log", "log ", "/log "} {
		e, err := parseExclude(p)
		if err != nil || e.match("log") || !e.match(strings.TrimPrefix(p, "/")) {
			t.Errorf("%q: %+v, %v; want spaces kept", p, e, err)
		}
	}
}

// literalDefaultFlags are the flags the built-in patterns must equal, as
// written in the help and the reference.
var literalDefaultFlags = []string{
	"--exclude", ".*",
	"--exclude", "*.tmp",
	"--exclude", "Thumbs.db",
	"--exclude", "desktop.ini",
	"--exclude", "$RECYCLE.BIN",
	"--exclude", "System Volume Information",
	"--exclude", "/log",
	"--exclude", "/tmp_stitch",
	"--exclude", "/batch_stack_tmp",
}

// selectionDevice is a share with something for every built-in pattern, and
// files that only the whole-share selection reaches. batch_stack_tmp cannot
// be listed.
func selectionDevice(t *testing.T) *device {
	t.Helper()
	old := time.Now().Add(-time.Hour)
	dev := newDevice()
	for _, p := range []string{
		"diag.png", "Autorun/Light/l.fit", "Autorun/Bias/b.fit", "Autorun/Dark/d.fit", "Preview/p.jpg",
		"MyWorks/M 31/log/x.txt", "MyWorks/M 31/Thumbs.db", "MyWorks/M 31/z.fit", "MyWorks/desktop.ini",
		"Video/x.mp4.tmp", "Video/y.mp4", "log/a.log", ".DS_Store", ".StackReplayTemp/s.fit",
		"tmp_stitch/t.fit", "batch_stack_tmp/q.fit", "$RECYCLE.BIN/r", "System Volume Information/v",
	} {
		dev.set(p, "abc", old)
	}
	dev.unreadable = []string{"batch_stack_tmp"}
	dev.use(t)
	return dev
}

// selectionCopied is what the defaults copy from selectionDevice.
var selectionCopied = []string{
	"Autorun/Bias/b.fit", "Autorun/Dark/d.fit", "Autorun/Light/l.fit", "MyWorks/M 31/log/x.txt",
	"MyWorks/M 31/z.fit", "Preview/p.jpg", "Video/y.mp4", "diag.png",
}

// localFiles lists the regular files under dest, slash-separated.
func localFiles(t *testing.T, dest string) []string {
	t.Helper()
	var got []string
	for p, desc := range snapshot(t, dest) {
		if !strings.HasPrefix(desc, "d") {
			rel, err := filepath.Rel(dest, p)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, filepath.ToSlash(rel))
		}
	}
	slices.Sort(got)
	return got
}

// The defaults copy the whole share except what they match, from root files
// to new top-level folders; a matched folder is never listed, so one that
// cannot be listed does not fail the pass. Each skipped entry is logged
// once, a folder without its contents. Without the defaults that folder is
// listed and fails the pass.
func TestSyncSelection(t *testing.T) {
	dev := selectionDevice(t)
	dest, stateDir := t.TempDir(), t.TempDir()
	code, _, stderr := invoke(t, "sync", "--endpoint", "h:445", "--dest", dest, "--state-dir", stateDir, "--once")
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if got := localFiles(t, dest); !slices.Equal(got, selectionCopied) {
		t.Errorf("copied %q, want %q", got, selectionCopied)
	}
	if got := slices.Sorted(maps.Keys(records(t, stateDir))); !slices.Equal(got, selectionCopied) {
		t.Errorf("recorded %q, want %q", got, selectionCopied)
	}
	checkLines(t, stderr, "msg=skipped ", selectionSkips("skipped"))
	if want := `msg="pass done" copied=8 replaced=0 adopted=0 waiting=0 failed=0 skipped=10 `; !strings.Contains(stderr, want) || strings.Contains(stderr, "would skip") {
		t.Errorf("log lacks %q or has a dry-run line:\n%s", want, stderr)
	}

	// --exclude adds to the defaults.
	dest = t.TempDir()
	code, _, stderr = invoke(t, "sync", "--endpoint", "h:445", "--dest", dest, "--state-dir", t.TempDir(), "--once",
		"--exclude", "/Autorun/Bias", "--exclude", "*.PNG")
	want := slices.DeleteFunc(slices.Clone(selectionCopied), func(p string) bool { return p == "Autorun/Bias/b.fit" || p == "diag.png" })
	if got := localFiles(t, dest); code != exitOK || !slices.Equal(got, want) {
		t.Errorf("with two more excludes: exit %d, copied %q, want %q\n%s", code, got, want, stderr)
	}

	dest = t.TempDir()
	code, _, stderr = invoke(t, "sync", "--endpoint", "h:445", "--dest", dest, "--state-dir", t.TempDir(), "--once", "--no-default-excludes")
	if want := `error="list the share: readdir batch_stack_tmp: permission denied"`; code != exitFailure || !strings.Contains(stderr, want) {
		t.Errorf("without the defaults: exit %d, log lacks %q:\n%s", code, want, stderr)
	}
	if got := localFiles(t, dest); len(got) != 0 {
		t.Errorf("a failed listing copied %q", got)
	}

	// An upgrade from 0.1.0, whose state names only files below its
	// --source folders. A recorded file since moved out of dest stays done
	// and one the defaults now exclude keeps its record. Newly covered
	// files old enough copy, or adopt a matching local file; younger ones
	// wait, which still exits 0.
	dest, stateDir = t.TempDir(), t.TempDir()
	statePath, err := state.Path(stateDir, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	v010 := fmt.Sprintf("{\"v\":1,\"dest\":%q}\n", dest) +
		`{"path":"MyWorks/exported.fit","size":3,"mtime":"1999-12-31T23:00:00Z","sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad","at":"1999-12-31T23:30:00Z","how":"copied"}` + "\n" +
		`{"path":"MyWorks/.hidden.fit","size":3,"mtime":"1999-12-31T23:00:00Z","sha256":"ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad","at":"1999-12-31T23:30:00Z","how":"copied"}` + "\n"
	if err := os.WriteFile(statePath, []byte(v010), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Date(1999, 12, 31, 23, 0, 0, 0, time.UTC)
	for _, p := range []string{"MyWorks/exported.fit", "MyWorks/.hidden.fit", "Autorun/Light/old.fit", "diag.png"} {
		dev.set(p, "abc", old)
	}
	dev.set("Autorun/Light/five.fit", "abc", time.Now().Add(-5*time.Minute))
	dev.set("Autorun/Light/one.fit", "abc", time.Now().Add(-time.Minute))
	place(t, dest, "diag.png", "abc", old)
	code, _, stderr = invoke(t, "sync", "--endpoint", "h:445", "--dest", dest, "--state-dir", stateDir, "--once")
	if want := "copied=8 replaced=0 adopted=1 waiting=2 failed=0 skipped=11 "; code != exitOK || !strings.Contains(stderr, want) {
		t.Errorf("upgrade: exit %d, log lacks %q:\n%s", code, want, stderr)
	}
	wantFiles := append(slices.Clone(selectionCopied), "Autorun/Light/old.fit")
	slices.Sort(wantFiles)
	if got := localFiles(t, dest); !slices.Equal(got, wantFiles) {
		t.Errorf("upgrade copied %q, want %q", got, wantFiles)
	}
	wantRecords := map[string]string{"MyWorks/exported.fit": "copied", "MyWorks/.hidden.fit": "copied", "Autorun/Light/old.fit": "copied"}
	for _, p := range selectionCopied {
		wantRecords[p] = "copied"
	}
	wantRecords["diag.png"] = "adopted"
	if got := records(t, stateDir); !maps.Equal(got, wantRecords) {
		t.Errorf("upgrade records %v, want %v", got, wantRecords)
	}
}

// A dry run names each skipped entry once, with the pattern that matched,
// and nothing below a skipped folder. It names each listed top-level folder
// with no copied files recorded under it in the state.
func TestDryRunReportsSelection(t *testing.T) {
	dev := selectionDevice(t)
	dest, stateDir := t.TempDir(), t.TempDir()
	code, _, stderr := dryRun(t, "--dest", dest, "--state-dir", stateDir)
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	skips := selectionSkips(`"would skip"`)
	folders := func(names ...string) []string {
		var lines []string
		for _, n := range names {
			lines = append(lines, `msg="no copied files recorded under this top-level folder" folder=`+n)
		}
		return lines
	}
	checkLines(t, stderr, "would skip", skips)
	checkLines(t, stderr, "no copied files recorded", folders("Autorun", "MyWorks", "Preview", "Video"))
	if want := `msg="dry run done" copy=8 replace=0 adopt=0 waiting=0 failed=0 skipped=10 `; !strings.Contains(stderr, want) || strings.Contains(stderr, "msg=skipped") {
		t.Errorf("log lacks %q or has a real-pass skip line:\n%s", want, stderr)
	}

	// After a real pass only a new folder has nothing recorded. Autorun
	// counts as recorded once any file under it is.
	readonly.Reset()
	if code, _, stderr := invoke(t, "sync", "--endpoint", "h:445", "--dest", dest, "--state-dir", stateDir, "--once", "--exclude", "/Autorun/Bias"); code != exitOK {
		t.Fatalf("real pass exit %d\n%s", code, stderr)
	}
	dev.set("Plan/x.json", "abc", time.Now().Add(-time.Hour))
	if err := os.Remove(filepath.Join(dest, "Preview", "p.jpg")); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = dryRun(t, "--dest", dest, "--state-dir", stateDir)
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	checkLines(t, stderr, "would skip", skips)
	checkLines(t, stderr, "no copied files recorded", folders("Plan"))
}

// A listing that fails after an exclude matched still fails the pass, copies
// nothing, and logs and counts the entry skipped before the failure, in a
// real pass and in a dry run.
func TestSkipsBeforeListingFailure(t *testing.T) {
	for _, tc := range []struct {
		name, flag, skip, summary string
	}{
		{"pass", "--once", `msg=skipped path=.hidden exclude=.*`, `msg="pass failed" copied=0 replaced=0 adopted=0 waiting=0 failed=0 skipped=1 `},
		{"dry run", "--dry-run", `msg="would skip" path=.hidden exclude=.*`, `msg="dry run failed" copy=0 replace=0 adopt=0 waiting=0 failed=0 skipped=1 `},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readonly.Reset()
			t.Cleanup(readonly.Reset)
			dev := newDevice()
			old := time.Now().Add(-time.Hour)
			dev.set(".hidden/inside.fit", "abc", old)
			dev.set("a.fit", "abc", old)
			dev.set("zbad/b.fit", "abc", old)
			dev.unreadable = []string{"zbad"}
			dev.use(t)
			dest := t.TempDir()
			code, _, stderr := invoke(t, "sync", "--endpoint", "h:445", "--dest", dest, "--state-dir", t.TempDir(), tc.flag)
			if code != exitFailure {
				t.Fatalf("exit %d, want %d\n%s", code, exitFailure, stderr)
			}
			for _, want := range []string{tc.skip, tc.summary, `error="list the share: `} {
				if !strings.Contains(stderr, want) {
					t.Errorf("log lacks %q:\n%s", want, stderr)
				}
			}
			if got := localFiles(t, dest); len(got) != 0 {
				t.Errorf("copied %v after a failed listing", got)
			}
		})
	}
}

// selectionSkips are the skip lines for selectionDevice under the defaults,
// with msg as the message.
func selectionSkips(msg string) []string {
	var lines []string
	for _, s := range []string{
		`path=$RECYCLE.BIN exclude=$RECYCLE.BIN`,
		`path=.DS_Store exclude=.*`,
		`path=.StackReplayTemp exclude=.*`,
		`path="MyWorks/M 31/Thumbs.db" exclude=Thumbs.db`,
		`path=MyWorks/desktop.ini exclude=desktop.ini`,
		`path="System Volume Information" exclude="System Volume Information"`,
		`path=Video/x.mp4.tmp exclude=*.tmp`,
		`path=batch_stack_tmp exclude=/batch_stack_tmp`,
		`path=log exclude=/log`,
		`path=tmp_stitch exclude=/tmp_stitch`,
	} {
		lines = append(lines, "msg="+msg+" "+s)
	}
	return lines
}

// checkLines checks that the lines of log containing key end with want, in
// order.
func checkLines(t *testing.T, log, key string, want []string) {
	t.Helper()
	var got []string
	for line := range strings.Lines(log) {
		if strings.Contains(line, key) {
			_, rest, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " level=INFO ")
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s lines:\n%s\nwant:\n%s", key, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The owner's rule: --no-default-excludes with the literal flags of the
// built-in list behaves exactly as the defaults, in a dry run and in a real
// pass, wherever --no-default-excludes is given.
func TestDefaultsEqualLiteralFlags(t *testing.T) {
	builtIn, err := parseExcludes(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var values []string
	for i := 1; i < len(literalDefaultFlags); i += 2 {
		values = append(values, literalDefaultFlags[i])
	}
	literal, err := parseExcludes(values, true)
	if err != nil || !slices.Equal(builtIn, literal) {
		t.Errorf("literal flags parse to %+v, %v; want the built-in %+v", literal, err, builtIn)
	}

	selectionDevice(t)
	variable := regexp.MustCompile(`time=\S+ |elapsed=\S+`)
	pass := func(dry bool, flags ...string) (string, []string) {
		t.Helper()
		dest, stateDir := t.TempDir(), t.TempDir()
		args := slices.Concat([]string{"--dest", dest, "--state-dir", stateDir, "--once"}, flags)
		var code int
		var stderr string
		if dry {
			code, _, stderr = dryRun(t, args...)
		} else {
			readonly.Reset()
			code, _, stderr = invoke(t, slices.Concat([]string{"sync", "--endpoint", "h:445"}, args)...)
		}
		if code != exitOK {
			t.Fatalf("%q: exit %d\n%s", flags, code, stderr)
		}
		// Workers finish files in any order.
		lines := slices.Sorted(strings.Lines(variable.ReplaceAllString(stderr, "")))
		return strings.Join(lines, ""), localFiles(t, dest)
	}
	for _, dry := range []bool{true, false} {
		wantLog, wantFiles := pass(dry)
		for _, flags := range [][]string{
			slices.Concat([]string{"--no-default-excludes"}, literalDefaultFlags),
			slices.Concat(literalDefaultFlags, []string{"--no-default-excludes"}),
			slices.Concat(literalDefaultFlags[:6], []string{"--no-default-excludes"}, literalDefaultFlags[6:]),
		} {
			gotLog, gotFiles := pass(dry, flags...)
			if gotLog != wantLog || !slices.Equal(gotFiles, wantFiles) {
				t.Errorf("dry run %t, %q:\n%s%q\nwant, as the defaults:\n%s%q", dry, flags, gotLog, gotFiles, wantLog, wantFiles)
			}
		}
	}
}
