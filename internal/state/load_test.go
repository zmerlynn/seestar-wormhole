package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-copy/internal/readonly"
)

// Load of a destination with no state directory and no destination folder
// gives no records and creates neither.
func TestLoadMissing(t *testing.T) {
	stateDir, dest := dirs(t)
	got, err := Load(stateDir, dest, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("Load = %v, %v; want no records", got, err)
	}
	notExist(t, stateDir)
	notExist(t, dest)
}

// Load of an empty state file gives no records, as Open would write a new
// header over it.
func TestLoadEmptyFile(t *testing.T) {
	stateDir, dest := dirs(t)
	mkdir(t, dest)
	writeState(t, stateDir, dest, "")
	if got, err := Load(stateDir, dest, nil); err != nil || len(got) != 0 {
		t.Fatalf("Load = %v, %v; want no records", got, err)
	}
}

// Load reads what Open reads, ignores a torn final line with a warning, and
// leaves the file as it was.
func TestLoadLeavesTornLine(t *testing.T) {
	stateDir, dest := dirs(t)
	mkdir(t, dest)
	content := headerLine(dest) + lineDark + lineM31Copied + `{"path":"MyWorks/M 31/Light_2.fit","si`
	p := writeState(t, stateDir, dest, content)
	log, logs := logBuffer()
	got, err := Load(stateDir, dest, log)
	if err != nil {
		t.Fatal(err)
	}
	if l := logs.String(); !strings.Contains(l, "level=WARN") || !strings.Contains(l, "torn final line") || !strings.Contains(l, "line=4") {
		t.Errorf("log = %q, want a warning naming line 4", l)
	}
	if got := readFile(t, p); got != content {
		t.Errorf("after Load the file is %q, want it unchanged", got)
	}
	want := mustOpen(t, stateDir, dest, nil).Snapshot()
	checkRecords(t, got, want)
}

// Load refuses what Open refuses without changing it: a header for another
// destination, a bad line, a state directory inside the destination, and
// another spelling of a registered destination.
func TestLoadRefuses(t *testing.T) {
	t.Run("other dest", func(t *testing.T) {
		stateDir, dest := dirs(t)
		mkdir(t, dest)
		writeState(t, stateDir, dest, headerLine("/elsewhere"))
		if _, err := Load(stateDir, dest, nil); err == nil || !strings.Contains(err.Error(), "not ") {
			t.Errorf("Load = %v, want the destination refused", err)
		}
	})
	t.Run("bad line", func(t *testing.T) {
		stateDir, dest := dirs(t)
		writeState(t, stateDir, dest, headerLine(dest)+"{nope\n"+lineDark)
		if _, err := Load(stateDir, dest, nil); err == nil || !strings.Contains(err.Error(), "line 2") {
			t.Errorf("Load = %v, want line 2 refused", err)
		}
	})
	t.Run("state inside dest", func(t *testing.T) {
		_, dest := dirs(t)
		mkdir(t, dest)
		if _, err := Load(filepath.Join(dest, "state"), dest, nil); err == nil {
			t.Error("Load with the state directory inside the destination succeeded")
		}
	})
	t.Run("alias", func(t *testing.T) {
		stateDir, real, link := aliasDirs(t)
		mustOpen(t, stateDir, real, nil).Close()
		if _, err := Load(stateDir, link, nil); err == nil || !strings.Contains(err.Error(), "same folder") {
			t.Errorf("Load of another spelling = %v, want it refused", err)
		}
	})
}

// After readonly.Enable, every function that writes the state panics, and
// nothing reaches the disk.
func TestWritesPanicWhenReadOnly(t *testing.T) {
	stateDir, dest := dirs(t)
	s := mustOpen(t, stateDir, dest, nil)
	p, _ := Path(stateDir, dest)
	before := readFile(t, p)
	other := filepath.Join(t.TempDir(), "other")
	readonly.Enable()
	t.Cleanup(readonly.Reset)
	for name, f := range map[string]func(){
		"state.Open":       func() { Open(stateDir, other, nil) },
		"state.openLocked": func() { openLocked(p, dest, nil) },
		"state.register":   func() { register(p, dest, nil) },
		"state.create":     func() { create(filepath.Join(stateDir, "new.jsonl"), dest) },
		"state.syncDir":    func() { syncDir(stateDir) },
		"state.lockFile":   func() { lockFile(filepath.Join(stateDir, "new.lock"), false) },
		"state.Append":     func() { s.Append(m31Copied(t)) },
	} {
		func() {
			defer func() {
				if r, _ := recover().(string); !strings.Contains(r, name+" would change the local filesystem") {
					t.Errorf("%s: panic %q, want the read-only guard", name, r)
				}
			}()
			f()
		}()
	}
	if got := readFile(t, p); got != before {
		t.Errorf("state file changed to %q", got)
	}
	notExist(t, other)
	for _, name := range []string{"new.jsonl", "new.lock"} {
		if _, err := os.Lstat(filepath.Join(stateDir, name)); err == nil {
			t.Errorf("%s was created", name)
		}
	}
}
