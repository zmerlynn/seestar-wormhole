package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// symlink makes a symbolic link or skips the test where it cannot.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot make a symbolic link: %v", err)
	}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func notExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s was created (%v)", p, err)
	}
}

// emptyOrMissing fails if anything was created in dir or at it.
func emptyOrMissing(t *testing.T, dir string) {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	for _, de := range des {
		t.Errorf("%s was created", filepath.Join(dir, de.Name()))
	}
}

func TestOpenCreatesDest(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "a", "b", "dest")
	mustOpen(t, filepath.Join(root, "state"), dest, nil)
	if fi, err := os.Stat(dest); err != nil || !fi.IsDir() {
		t.Errorf("destination after Open: %v, %v, want a directory", fi, err)
	}

	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state2")
	if s, err := Open(stateDir, file, nil); err == nil || !strings.Contains(err.Error(), "create destination") {
		if s != nil {
			s.Close()
		}
		t.Errorf("Open with a file as the destination = %v, want a create error", err)
	}
	notExist(t, stateDir)
}

func TestOpenRefusesStateInsideDest(t *testing.T) {
	for _, tc := range []struct {
		name string
		// setup makes the links under root and returns the state directory,
		// and a directory in dest that must stay empty or missing.
		setup func(t *testing.T, root, dest string) (stateDir, untouched string)
		want  string
	}{
		{"by its path", func(t *testing.T, root, dest string) (string, string) {
			return filepath.Join(dest, "state"), filepath.Join(dest, "state")
		}, "is inside the destination"},
		{"a link into dest", func(t *testing.T, root, dest string) (string, string) {
			mkdir(t, filepath.Join(dest, "inner"))
			symlink(t, filepath.Join(dest, "inner"), filepath.Join(root, "state"))
			return filepath.Join(root, "state"), filepath.Join(dest, "inner")
		}, "resolves to"},
		{"a link to dest", func(t *testing.T, root, dest string) (string, string) {
			mkdir(t, dest)
			symlink(t, dest, filepath.Join(root, "state"))
			return filepath.Join(root, "state"), dest
		}, "resolves to"},
		{"under a link to dest", func(t *testing.T, root, dest string) (string, string) {
			mkdir(t, dest)
			symlink(t, dest, filepath.Join(root, "link"))
			return filepath.Join(root, "link", "state"), dest
		}, "resolves to"},
		{"a sync link into dest", func(t *testing.T, root, dest string) (string, string) {
			mkdir(t, filepath.Join(dest, "inner"))
			mkdir(t, filepath.Join(root, "state"))
			symlink(t, filepath.Join(dest, "inner"), filepath.Join(root, "state", "sync"))
			return filepath.Join(root, "state"), filepath.Join(dest, "inner")
		}, "resolves to"},
		{"a dangling link into dest", func(t *testing.T, root, dest string) (string, string) {
			mkdir(t, dest)
			symlink(t, filepath.Join(dest, "missing"), filepath.Join(root, "state"))
			return filepath.Join(root, "state"), filepath.Join(dest, "missing")
		}, "resolve state directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dest := filepath.Join(root, "dest")
			stateDir, untouched := tc.setup(t, root, dest)
			s, err := Open(stateDir, dest, nil)
			if err == nil {
				s.Close()
				t.Fatal("Open succeeded, want a refusal")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), stateDir) {
				t.Errorf("Open error = %q, want it to contain %q and %s", err, tc.want, stateDir)
			}
			emptyOrMissing(t, untouched)
		})
	}
}

// TestOpenRefusesStateUnderCaseAlias runs only where the temporary directory
// is on a case-insensitive filesystem, such as the default on macOS.
func TestOpenRefusesStateUnderCaseAlias(t *testing.T) {
	root := t.TempDir()
	dest, alias := filepath.Join(root, "Photos"), filepath.Join(root, "photos")
	mkdir(t, dest)
	want, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.Stat(alias); err != nil || !os.SameFile(got, want) {
		t.Skip("the temporary directory is on a case-sensitive filesystem")
	}
	stateDir := filepath.Join(alias, "state")
	s, err := Open(stateDir, dest, nil)
	if err == nil {
		s.Close()
		t.Fatal("Open put the state under another case of the destination")
	}
	if !strings.Contains(err.Error(), "is inside the destination "+dest) {
		t.Errorf("Open error = %q, want it to say the state is inside %s", err, dest)
	}
	notExist(t, filepath.Join(dest, "state"))
}

func TestOpenAllowsLinkedStateOutsideDest(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, "real-state"))
	mkdir(t, filepath.Join(root, "real-dest"))
	// A destination whose name the state directory's resolved path starts
	// with is still outside it.
	// The targets exist first: Windows makes a link to a missing target a
	// file link, which does not work as a folder.
	mkdir(t, filepath.Join(root, "real-dest-state"))
	symlink(t, filepath.Join(root, "real-dest-state"), filepath.Join(root, "state"))
	symlink(t, filepath.Join(root, "real-dest"), filepath.Join(root, "dest"))
	mustOpen(t, filepath.Join(root, "state"), filepath.Join(root, "dest"), nil)
	if _, err := os.Stat(filepath.Join(root, "real-dest-state", "sync")); err != nil {
		t.Errorf("sync directory not created through the link: %v", err)
	}
}

// aliasDirs returns a state directory, a destination folder, and a symbolic
// link to that folder.
func aliasDirs(t *testing.T) (stateDir, real, link string) {
	t.Helper()
	root := t.TempDir()
	real, link = filepath.Join(root, "real"), filepath.Join(root, "link")
	mkdir(t, real)
	symlink(t, real, link)
	return filepath.Join(root, "state"), real, link
}

func TestOpenRefusesAlias(t *testing.T) {
	stateDir, real, link := aliasDirs(t)
	first := mustOpen(t, stateDir, real, nil)
	if err := first.Append(m31Copied(t)); err != nil {
		t.Fatal(err)
	}
	realState, _ := Path(stateDir, real)
	before := readFile(t, realState)
	linkState, _ := Path(stateDir, link)
	// Whether or not the first is still open.
	for _, open := range []bool{true, false} {
		if !open {
			first.Close()
		}
		s, err := Open(stateDir, link, nil)
		if err == nil {
			s.Close()
			t.Fatalf("open=%v: Open of a link to an opened destination succeeded", open)
		}
		want := "destination " + link + " is the same folder as " + real + ", which already has the state file " + realState
		if errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), want) {
			t.Errorf("open=%v: Open error = %q, want it to contain %q", open, err, want)
		}
		notExist(t, linkState)
		checkUnlocked(t, stateDir, link)
	}
	if got := readFile(t, realState); got != before {
		t.Errorf("the other state file changed to %q", got)
	}
}

func TestOpenFindsSplitHistory(t *testing.T) {
	stateDir, real, link := aliasDirs(t)
	realContent, linkContent := headerLine(real)+lineM31Copied, headerLine(link)+lineDark
	realState := writeState(t, stateDir, real, realContent)
	linkState := writeState(t, stateDir, link, linkContent)
	for _, tc := range []struct{ dest, other, otherState string }{
		{real, link, linkState},
		{link, real, realState},
	} {
		s, err := Open(stateDir, tc.dest, nil)
		if err == nil {
			s.Close()
			t.Fatalf("Open(%s) succeeded with a second history for the same folder", tc.dest)
		}
		if want := "is the same folder as " + tc.other + ", which already has the state file " + tc.otherState; !strings.Contains(err.Error(), want) {
			t.Errorf("Open(%s) error = %q, want it to contain %q", tc.dest, err, want)
		}
	}
	if readFile(t, realState) != realContent || readFile(t, linkState) != linkContent {
		t.Error("a refused open changed a state file")
	}
}

// TestOpenRefusesAliasOfFutureHistory checks that a header of a version this
// release cannot read still counts: its folder is taken.
func TestOpenRefusesAliasOfFutureHistory(t *testing.T) {
	stateDir, real, link := aliasDirs(t)
	q, err := json.Marshal(real)
	if err != nil {
		t.Fatal(err)
	}
	content := `{"v":2,"dest":` + string(q) + "}\n" + lineDark
	realState := writeState(t, stateDir, real, content)
	linkState, _ := Path(stateDir, link)
	want := "destination " + link + " is the same folder as " + real + ", which already has the state file " + realState
	// A second try must reach the check again, not find a lock left held.
	for try := 1; try <= 2; try++ {
		s, err := Open(stateDir, link, nil)
		if err == nil {
			s.Close()
			t.Fatalf("try %d: Open of a link to a folder with a version 2 history succeeded", try)
		}
		if errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), want) {
			t.Errorf("try %d: Open error = %q, want it to contain %q", try, err, want)
		}
		notExist(t, linkState)
		for _, name := range syncDirEntries(t, stateDir) {
			if strings.HasSuffix(name, ".tmp") {
				t.Errorf("try %d: %s was left in the sync directory", try, name)
			}
		}
		checkUnlocked(t, stateDir, link)
	}
	// The original spelling is refused for its version, not for a lock.
	s, err := Open(stateDir, real, nil)
	if err == nil {
		s.Close()
		t.Fatal("Open of a version 2 history succeeded")
	}
	if errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "state version 2 is not supported") {
		t.Errorf("Open(%s) error = %q, want the version refusal", real, err)
	}
	checkUnlocked(t, stateDir, real)
	if got := readFile(t, realState); got != content {
		t.Errorf("the version 2 state file changed to %q", got)
	}
}

func TestOpenSkipsMissingOtherDest(t *testing.T) {
	root := t.TempDir()
	stateDir, gone, dest := filepath.Join(root, "state"), filepath.Join(root, "gone"), filepath.Join(root, "dest")
	s := mustOpen(t, stateDir, gone, nil)
	s.Close()
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	goneState, _ := Path(stateDir, gone)
	log, logs := logBuffer()
	mustOpen(t, stateDir, dest, log)
	if got := readFile(t, goneState); got != headerLine(gone) {
		t.Errorf("the other state file changed to %q", got)
	}
	if got := logs.String(); !strings.Contains(got, "level=DEBUG") || !strings.Contains(got, "file="+goneState) || strings.Contains(got, "WARN") {
		t.Errorf("log = %q, want one debug line naming %s", got, goneState)
	}
}

func TestOpenTwoDests(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	log, logs := logBuffer()
	one := mustOpen(t, stateDir, filepath.Join(root, "one"), log)
	two := mustOpen(t, stateDir, filepath.Join(root, "two"), log)
	one.Close()
	two.Close()
	// Each is checked against the other again when its state exists.
	mustOpen(t, stateDir, filepath.Join(root, "one"), log)
	mustOpen(t, stateDir, filepath.Join(root, "two"), log)
	if logs.Len() != 0 {
		t.Errorf("log = %q, want nothing", logs.String())
	}
}

func TestOpenSkipsMalformedOtherState(t *testing.T) {
	stateDir, dest := dirs(t)
	syncDir := filepath.Join(stateDir, "sync")
	mkdir(t, syncDir)
	others := map[string]string{
		"garbled.jsonl":    "{nope\n",
		"empty.jsonl":      "",
		"no-newline.jsonl": headerLine(dest)[:10],
		"relative.jsonl":   `{"v":1,"dest":"dest"}` + "\n",
		"no-version.jsonl": `{"dest":` + strings.TrimPrefix(headerLine(dest), `{"v":1,"dest":`),
	}
	for name, content := range others {
		if err := os.WriteFile(filepath.Join(syncDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mkdir(t, filepath.Join(syncDir, "dir.jsonl"))
	log, logs := logBuffer()
	mustOpen(t, stateDir, dest, log)
	got := logs.String()
	for name, content := range others {
		if !strings.Contains(got, "file="+filepath.Join(syncDir, name)) {
			t.Errorf("no warning for %s in log %q", name, got)
		}
		if readFile(t, filepath.Join(syncDir, name)) != content {
			t.Errorf("%s changed", name)
		}
	}
	if n := strings.Count(got, "level=WARN"); n != len(others) {
		t.Errorf("%d warnings, want %d: %q", n, len(others), got)
	}
}

func TestOpenRegistrationSerializes(t *testing.T) {
	stateDir, real, link := aliasDirs(t)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	registerHook = func() {
		entered <- struct{}{}
		<-release
	}
	t.Cleanup(func() { registerHook = nil })

	type result struct {
		s   *State
		err error
	}
	open := func(dest string) chan result {
		c := make(chan result, 1)
		go func() {
			s, err := Open(stateDir, dest, nil)
			c <- result{s, err}
		}()
		return c
	}
	first := open(real)
	<-entered
	// The first open is between its check and its header: it holds the
	// registration lock.
	if f, err := lockFile(filepath.Join(stateDir, "sync", registryName), false); !errors.Is(err, ErrLocked) {
		if f != nil {
			f.Close()
		}
		t.Errorf("registration lock during registration: %v, want ErrLocked", err)
	}
	second := open(link)
	select {
	case <-entered:
		t.Error("a second spelling passed the check while the first was registering")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	r := <-first
	if r.err != nil {
		t.Fatalf("first Open: %v", r.err)
	}
	defer r.s.Close()
	r = <-second
	if r.err == nil {
		r.s.Close()
		t.Fatal("both spellings of one folder opened")
	}
	if !strings.Contains(r.err.Error(), "is the same folder as "+real) {
		t.Errorf("second Open error = %q, want the alias refusal", r.err)
	}
}
