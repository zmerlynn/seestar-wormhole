package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testSettings() Settings { s := defaultSettings(); s.Email = "synthetic@example.invalid"; return s }
func testStorage(t *testing.T) *storage {
	t.Helper()
	s, err := openStorage(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.directory.Close() })
	return s
}
func TestSettingsStorage(t *testing.T) {
	st := testStorage(t)
	for _, body := range []string{`{`, `{"version":99}`, `{}`, `null`, `{"version":1,"idle":"-1s"}`, `{"version":1,"idle":120}`, `{"version":1,"ports":[{"local":"80","device":80}]}`, `{"version":1,"ports":["4445:80"]}`, `{"version":1,"serial":"\n"}`, `{"version":1,"email":"invalid"}`, `{"version":1,"version":1}`, strings.Repeat(" ", maxJSON+1)} {
		path := filepath.Join(st.root, "config.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSettings(st); err == nil {
			t.Fatalf("bad settings accepted: %.100q", body)
		}
	}
}

func TestHeadlessSettingsReadOnly(t *testing.T) {
	st := testStorage(t)
	if err := st.writeBytes("config.json", []byte(`{"version":1,"future":{"keep":true}}`)); err != nil {
		t.Fatal(err)
	}
	lockFile, err := os.OpenFile(filepath.Join(st.root, "ui.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lockFile.Close()
	if locked, err := tryLock(lockFile); err != nil || !locked {
		t.Fatalf("lock: %v", err)
	}
	before, err := os.Stat(filepath.Join(st.root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSettings(st.root); err != nil {
		t.Fatalf("load while UI holds lock: %v", err)
	}
	after, _ := os.Stat(filepath.Join(st.root, "config.json"))
	if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("config changed during read")
	}
	entries, _ := os.ReadDir(st.root)
	if len(entries) != 2 {
		t.Fatalf("unexpected files: %v", entries)
	}
	missing := filepath.Join(t.TempDir(), "absent")
	if _, err := LoadSettings(missing); err == nil {
		t.Fatal("missing root accepted")
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Fatal("read created state root")
	}
	for _, body := range []string{`{"version":99}`, `{}`, strings.Repeat(" ", maxJSON+1)} {
		if err := os.WriteFile(filepath.Join(st.root, "config.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSettings(st.root); err == nil {
			t.Fatal("invalid headless config accepted")
		}
	}
}
