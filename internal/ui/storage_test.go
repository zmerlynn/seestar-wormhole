package ui

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
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
	want.Ports = []string{"1445:445", "0:80"}
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
	for _, body := range []string{`{"version":1,"bind":"127.0.0.1","idle":"0","future":{"nested":true}}`, `{"version":1,"future":3}`} {
		if err := st.write("config.json", jsontext.Value(body)); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSettings(st); err != nil {
			t.Fatalf("unknown members refused: %v", err)
		}
	}
	for _, body := range []string{`{`, `{"version":2}`, `{}`, `null`, `{"version":1,"idle":"-1s"}`, `{"version":1,"ports":["4445:80"]}`, `{"version":1,"serial":"\n"}`, `{"version":1,"email":"invalid"}`, `{"version":1,"version":1}`, strings.Repeat(" ", maxJSON+1)} {
		path := filepath.Join(st.root, "config.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSettings(st); err == nil {
			t.Fatalf("bad settings accepted: %.100q", body)
		}
	}
}
