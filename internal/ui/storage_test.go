package ui

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
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
	want := testSettings()
	want.Idle = "2m"
	want.Ports = []proxyrun.Port{{Local: 1445, Device: 445}, {Local: 0, Device: 80}}
	want.Remember = true
	if err := st.write("config.json", want); err != nil {
		t.Fatal(err)
	}
	got, err := loadSettings(st)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(got)
	if !bytes.Equal(a, b) {
		t.Fatalf("%s != %s", a, b)
	}
	for _, body := range []string{`{"version":2,"bind":"127.0.0.1","idle":"0","future":{"nested":true}}`, `{"version":2,"future":3}`} {
		if err := st.write("config.json", jsontext.Value(body)); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSettings(st); err != nil {
			t.Fatalf("unknown members refused: %v", err)
		}
	}
	for _, body := range []string{`{`, `{"version":99}`, `{}`, `null`, `{"version":2,"idle":"-1s"}`, `{"version":2,"ports":["4445:80"]}`, `{"version":2,"serial":"\n"}`, `{"version":2,"email":"invalid"}`, `{"version":2,"version":2}`, strings.Repeat(" ", maxJSON+1)} {
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
	if err := st.write("config.json", testSettings()); err != nil {
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
