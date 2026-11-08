package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("HOME", base)
	t.Setenv("AppData", base)
	want, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	want = filepath.Join(want, "seestar-copy")
	for _, f := range []func() (string, error){DefaultDir, func() (string, error) { return HistoryDir("") }} {
		if got, err := f(); err != nil || got != want {
			t.Errorf("default history dir %q, %v, want %q", got, err, want)
		}
	}
	if got, err := HistoryDir("x"); err != nil || got != "x" {
		t.Errorf("explicit history dir %q, %v, want x", got, err)
	}
	// With no per-user config folder, the error names the flag to give.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("AppData", "")
	if got, err := HistoryDir(""); err == nil || err.Error() != "no per-user config folder; give --history-dir" {
		t.Errorf("no config folder: %q, %v", got, err)
	}
	if got, err := HistoryDir("x"); err != nil || got != "x" {
		t.Errorf("explicit history dir without a config folder %q, %v, want x", got, err)
	}
}
