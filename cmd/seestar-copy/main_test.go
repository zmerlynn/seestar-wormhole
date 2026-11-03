package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolate points os.UserConfigDir at a temp folder on every OS and returns
// the default settings file there.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("AppData", dir)
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(base, "seestar-copy", "app.json")
}

// cancelOnWrite cancels once the app prints its URL.
type cancelOnWrite struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelOnWrite) Write(p []byte) (int, error) {
	defer w.cancel()
	return w.Buffer.Write(p)
}

func TestArgs(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{{"x"}, {"--endpoint", "a"}, {"--settings"}, {"--settings="}, {"--state-dir", "x"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestStartupError(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "mine.json")
	if err := os.WriteFile(path, []byte(`{"version":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--settings", path}, &stdout, &stderr)
	want := "Couldn't read settings from " + path + ": it has version 3, and this seestar-copy reads version 1. Fix or remove the file, then reopen seestar-copy.\n"
	if code != 1 || stdout.Len() != 0 || stderr.String() != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want 1 and %q", code, stdout.String(), stderr.String(), want)
	}
}

func TestSettingsPath(t *testing.T) {
	def := isolate(t)
	// The default file is read when --settings is absent, and not when it
	// names another file.
	if err := os.MkdirAll(filepath.Dir(def), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(def, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := run(context.Background(), nil, &bytes.Buffer{}, &stderr); code != 1 || !strings.HasPrefix(stderr.String(), "Couldn't read settings from "+def+": ") {
		t.Errorf("default: exit %d, %q", code, stderr.String())
	}
	other := filepath.Join(t.TempDir(), "other.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := &cancelOnWrite{cancel: cancel}
	stderr.Reset()
	if code := run(ctx, []string{"--settings", other}, stdout, &stderr); code != 0 {
		t.Fatalf("--settings: exit %d, %q", code, stderr.String())
	}
	if url := stdout.String(); !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.Contains(url, "/app/#") || !strings.HasSuffix(url, "\n") {
		t.Errorf("printed %q, want the page's URL", url)
	}
	if !strings.Contains(stderr.String(), "settings="+other) {
		t.Errorf("the log does not name %s:\n%s", other, stderr.String())
	}
}
