package ui

import (
	"bytes"
	"encoding/json/v2"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// fallbackLog is the warning a load's fallbacks log, as the app writes it.
func fallbackLog(t *testing.T, fallbacks []diagnostic.Fallback) string {
	t.Helper()
	var buf bytes.Buffer
	diagnostic.LogFallbacks(diagnostic.Logger("json", &buf, slog.LevelWarn), fallbacks)
	return buf.String()
}

// checkFallback asserts one fallback for setting, logged once as a warning
// naming the setting and its fallback, without the saved value.
func checkFallback(t *testing.T, fallbacks []diagnostic.Fallback, file, setting, fallback, private string) {
	t.Helper()
	if len(fallbacks) != 1 || fallbacks[0] != (diagnostic.Fallback{File: file, Setting: setting, Fallback: fallback}) {
		t.Fatalf("fallbacks = %+v", fallbacks)
	}
	log := fallbackLog(t, fallbacks)
	lines := strings.Split(strings.TrimSpace(log), "\n")
	if len(lines) != 1 {
		t.Fatalf("log = %q; want one line", log)
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &line); err != nil {
		t.Fatal(err)
	}
	if line["level"] != "WARN" || line["msg"] != "settings_fallback" || line["file"] != file || line["setting"] != setting || line["fallback"] != fallback {
		t.Fatalf("log line = %v", line)
	}
	if strings.Contains(log, private) {
		t.Fatalf("log names the saved value: %s", log)
	}
}

// A bad config.json value loads as its fallback and is logged, not shown.
func TestSettingsFallbacks(t *testing.T) {
	for _, tc := range []struct {
		setting, body, fallback, private string
		check                            func(Settings) bool
	}{
		{"discovery", `{"version":1,"bind":"127.0.0.9","discovery":true}`, "off", "127.0.0.9", func(s Settings) bool { return !s.Discovery && s.Bind == "127.0.0.9" }},
		{"serial", `{"version":1,"serial":"secret serial\n"}`, "none", "secret serial", func(s Settings) bool { return s.Serial == "" }},
		{"email", `{"version":1,"email":"private-not-an-email"}`, "none", "private-not-an-email", func(s Settings) bool { return s.Email == "" }},
	} {
		t.Run(tc.setting+" "+tc.private, func(t *testing.T) {
			st := testStorage(t)
			if err := os.WriteFile(filepath.Join(st.root, "config.json"), []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			s, fallbacks, err := loadSettings(st)
			if err != nil || st.settingsRefused || !tc.check(s) {
				t.Fatalf("load = %+v, %v, refused %v", s, err, st.settingsRefused)
			}
			checkFallback(t, fallbacks, "config.json", tc.setting, tc.fallback, tc.private)
			// proxy --config reads it the same way.
			s, fallbacks, err = LoadSettings(st.root)
			if err != nil || !tc.check(s) || len(fallbacks) != 1 {
				t.Fatalf("LoadSettings = %+v, %v, %v", s, fallbacks, err)
			}
			// Saving from the page stays strict.
			if tc.setting == "serial" || tc.setting == "email" {
				bad := testSettings()
				if tc.setting == "serial" {
					bad.Serial = "secret serial\n"
				} else {
					bad.Email = "private-not-an-email"
				}
				if _, _, err := bad.validate(); err == nil || actionError(err).Field != tc.setting {
					t.Fatalf("typed %s accepted: %v", tc.setting, err)
				}
			}
		})
	}
}

// A good config.json has no fallbacks; a bad version, a file that doesn't
// parse, and a bad idle, bind, model or port are still refused.
func TestSettingsStillRefused(t *testing.T) {
	st := testStorage(t)
	if err := st.writeBytes("config.json", []byte(`{"version":1,"idle":"10m","serial":"S50ABC"}`)); err != nil {
		t.Fatal(err)
	}
	if _, fallbacks, err := loadSettings(st); err != nil || len(fallbacks) != 0 {
		t.Fatalf("good settings = %v, %v", fallbacks, err)
	}
	for _, body := range []string{
		`{"version":2,"email":"private-not-an-email"}`, `{"email":"private-not-an-email"}`, `{"version":1,"email":"private-not-an-email"`, `{"version":1,"idle":120}`,
		`{"version":1,"idle":"soon"}`, `{"version":1,"idle":"-17m"}`, `{"version":1,"bind":"  "}`, `{"version":1,"model":"secret\nmodel"}`,
		`{"version":1,"ports":[{"local":80,"device":0}]}`,
	} {
		st := testStorage(t)
		if err := os.WriteFile(filepath.Join(st.root, "config.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadSettings(st); err == nil || !st.settingsRefused {
			t.Fatalf("%s accepted", body)
		}
	}
}

// A files_at_once outside 1..MaxFilesAtOnce is clamped into range and
// logged; another version or a relative history folder is still refused.
func TestCopySettingsFallbacks(t *testing.T) {
	for _, tc := range []struct {
		body, fallback, private string
		want                    int
	}{
		{`{"version":1,"folder":"/synthetic/Seestar","files_at_once":0}`, "1", "/synthetic/Seestar", 1},
		{`{"version":1,"folder":"/synthetic/Seestar","files_at_once":-3}`, "1", "-3", 1},
		{`{"version":1,"folder":"/synthetic/Seestar","files_at_once":57}`, "4", "57", 4},
	} {
		t.Run(tc.body, func(t *testing.T) {
			st := testStorageWith(t, tc.body)
			_, set, fallbacks, err := loadCopySettings(st)
			if err != nil || set == nil || set.FilesAtOnce != tc.want {
				t.Fatalf("load = %+v, %v", set, err)
			}
			checkFallback(t, fallbacks, copyName, "files_at_once", tc.fallback, tc.private)
			loaded, fallbacks, err := LoadCopySettings(st.root)
			if err != nil || loaded.FilesAtOnce != tc.want || len(fallbacks) != 1 {
				t.Fatalf("LoadCopySettings = %+v, %v, %v", loaded, fallbacks, err)
			}
		})
	}
	for _, body := range []string{`{"version":2,"files_at_once":0}`, `{"version":1,"files_at_once":0,"history_dir":"synthetic/history"}`, `{"version":1,"files_at_once":0`} {
		if _, _, _, err := loadCopySettings(testStorageWith(t, body)); err == nil {
			t.Fatalf("%s accepted", body)
		}
	}
	if _, _, fallbacks, err := loadCopySettings(testStorageWith(t, `{"version":1,"files_at_once":4}`)); err != nil || len(fallbacks) != 0 {
		t.Fatalf("in range = %v, %v", fallbacks, err)
	}
}
