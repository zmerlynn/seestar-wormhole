package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// config sets the per-user config folder to a new temporary folder and
// returns it.
func config(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("HOME", base)
	t.Setenv("AppData", base)
	got, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// folders returns the names in dir, and in dir/seestar-wormhole.
func folders(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	for _, d := range []string{dir, filepath.Join(dir, "seestar-wormhole")} {
		entries, err := os.ReadDir(d)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		for _, e := range entries {
			rel, _ := filepath.Rel(dir, filepath.Join(d, e.Name()))
			names = append(names, filepath.ToSlash(rel))
		}
	}
	return names
}

// records decodes the JSON log records in logs.
func records(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var got []map[string]any
	for dec := json.NewDecoder(logs); dec.More(); {
		var r map[string]any
		if err := dec.Decode(&r); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	return got
}

// withoutTime returns r without its time.
func withoutTime(r map[string]any) map[string]any {
	r = maps.Clone(r)
	delete(r, slog.TimeKey)
	return r
}

// writeOld writes a file named "history" in the old default folder.
func writeOld(t *testing.T, base, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(base, "seestar-copy"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "seestar-copy", "history"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryDir(t *testing.T) {
	want := filepath.Join(config(t), "seestar-wormhole", "copy-history")
	for _, f := range []func() (string, error){DefaultDir, func() (string, error) { return HistoryDir("", nil) }, func() (string, error) { return ReadHistoryDir("") }} {
		if got, err := f(); err != nil || got != want {
			t.Errorf("default history dir %q, %v, want %q", got, err, want)
		}
	}
	for _, f := range []func(string) (string, error){func(s string) (string, error) { return HistoryDir(s, nil) }, ReadHistoryDir} {
		if got, err := f("x"); err != nil || got != "x" {
			t.Errorf("explicit history dir %q, %v, want x", got, err)
		}
	}
	// With no per-user config folder, the error names the flag to give.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("AppData", "")
	for _, f := range []func(string) (string, error){func(s string) (string, error) { return HistoryDir(s, nil) }, ReadHistoryDir} {
		if got, err := f(""); err == nil || err.Error() != "no per-user config folder; give --history-dir" {
			t.Errorf("no config folder: %q, %v", got, err)
		}
		if got, err := f("x"); err != nil || got != "x" {
			t.Errorf("explicit history dir without a config folder %q, %v, want x", got, err)
		}
	}
}

// While only the old folder exists, a read reads it in place, and a run
// that writes moves it once, logging the move.
func TestOldHistoryMoves(t *testing.T) {
	base := config(t)
	writeOld(t, base, "saved")
	def, _ := DefaultDir()
	old := filepath.Join(base, "seestar-copy")

	if got := HistoryToRead(def); got != old {
		t.Errorf("read %q, want the old folder %q", got, old)
	}
	if got, err := ReadHistoryDir(""); err != nil || got != old {
		t.Errorf("read history dir %q, %v, want %q", got, err, old)
	}
	if got := folders(t, base); !slices.Equal(got, []string{"seestar-copy"}) {
		t.Errorf("after reads the config folder holds %q, want only seestar-copy", got)
	}

	var logs bytes.Buffer
	got, err := HistoryDir("", slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil || got != def {
		t.Fatalf("history dir %q, %v, want %q", got, err, def)
	}
	if b, err := os.ReadFile(filepath.Join(def, "history")); err != nil || string(b) != "saved" {
		t.Errorf("moved history %q, %v, want saved", b, err)
	}
	if got := folders(t, base); !slices.Equal(got, []string{"seestar-wormhole", "seestar-wormhole/copy-history"}) {
		t.Errorf("after the move the config folder holds %q", got)
	}
	if fi, err := os.Lstat(filepath.Dir(def)); err != nil || !fi.IsDir() {
		t.Errorf("seestar-wormhole: %v, %v", fi, err)
	}
	want := map[string]any{"level": "INFO", "msg": "moved the copy history", "from": old, "to": def}
	if got := records(t, &logs); len(got) != 1 || !maps.Equal(withoutTime(got[0]), want) {
		t.Errorf("log %v, want one record %v", got, want)
	}

	// The move happens once; then reads and writes use the new folder.
	logs.Reset()
	if err := MoveOldHistory(def, slog.New(slog.NewTextHandler(&logs, nil))); err != nil || logs.Len() != 0 {
		t.Errorf("second move: %v, logged %q", err, logs.String())
	}
	if got := HistoryToRead(def); got != def {
		t.Errorf("read %q after the move, want %q", got, def)
	}
}

// When both folders exist the new one is used and the old one is left
// alone; a folder other than the default is never a move's target.
func TestOldHistoryStays(t *testing.T) {
	base := config(t)
	writeOld(t, base, "old")
	def, _ := DefaultDir()
	other := filepath.Join(t.TempDir(), "copy-history")
	if err := MoveOldHistory(other, nil); err != nil {
		t.Fatal(err)
	}
	if got := HistoryToRead(other); got != other {
		t.Errorf("read %q, want %q", got, other)
	}
	if got := folders(t, base); !slices.Equal(got, []string{"seestar-copy"}) {
		t.Errorf("with another folder the config folder holds %q, want only seestar-copy", got)
	}

	if err := os.MkdirAll(def, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, err := HistoryDir("", nil); err != nil || got != def {
		t.Errorf("history dir %q, %v, want %q", got, err, def)
	}
	if got := HistoryToRead(def); got != def {
		t.Errorf("read %q, want %q", got, def)
	}
	if b, err := os.ReadFile(filepath.Join(base, "seestar-copy", "history")); err != nil || string(b) != "old" {
		t.Errorf("old history %q, %v, want it left alone", b, err)
	}
	if _, err := os.Lstat(filepath.Join(def, "history")); !os.IsNotExist(err) {
		t.Errorf("the new folder gained the old history: %v", err)
	}
}

// A rename that fails stops the run with an error naming both folders;
// one that loses a race to another process finds the new folder in place.
func TestOldHistoryRenameFails(t *testing.T) {
	base := config(t)
	writeOld(t, base, "old")
	def, _ := DefaultDir()
	old := filepath.Join(base, "seestar-copy")
	t.Cleanup(func() { rename = os.Rename })

	rename = func(string, string) error { return errors.New("in use") }
	got, err := HistoryDir("", nil)
	if err == nil || got != "" {
		t.Fatalf("history dir %q, %v, want an error", got, err)
	}
	for _, want := range []string{old, def, "in use", "try again", "move the folder by hand"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if b, err := os.ReadFile(filepath.Join(old, "history")); err != nil || string(b) != "old" {
		t.Errorf("old history %q, %v, want it left alone", b, err)
	}

	// Another process moves the folder between the check and the rename.
	rename = func(from, to string) error {
		if err := os.Rename(from, to); err != nil {
			t.Fatal(err)
		}
		return errors.New("lost the race")
	}
	var logs bytes.Buffer
	if got, err := HistoryDir("", slog.New(slog.NewTextHandler(&logs, nil))); err != nil || got != def {
		t.Errorf("after a lost race: %q, %v, want %q", got, err, def)
	}
	if logs.Len() != 0 {
		t.Errorf("the loser logged %q", logs.String())
	}
}
