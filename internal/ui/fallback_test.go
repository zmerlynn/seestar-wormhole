package ui

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/testlog"
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
	checkFallbackLog(t, fallbackLog(t, fallbacks), file, setting, fallback, private)
}

// checkFallbackLog asserts log is one warning naming the setting and its
// fallback, and that none of its fields but the time holds private.
func checkFallbackLog(t *testing.T, log, file, setting, fallback, private string) {
	t.Helper()
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
	// The time is left out: its digits can match a numeric value.
	delete(line, "time")
	fields, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(fields), private) {
		t.Fatalf("log names the saved value: %s", log)
	}
}

// A timestamp holding a saved number's digits isn't a leak of it.
func TestFallbackLogIgnoresTime(t *testing.T) {
	var line map[string]any
	log := fallbackLog(t, []diagnostic.Fallback{{File: copyName, Setting: "files_at_once", Fallback: "4"}})
	if err := json.Unmarshal([]byte(log), &line); err != nil {
		t.Fatal(err)
	}
	line["time"] = "2057-01-01T00:00:00.000Z"
	fixed, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	checkFallbackLog(t, string(fixed), copyName, "files_at_once", "4", "57")
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

// The app logs each fallback in both files once in its log, without the
// saved value.
func TestAppLogsFallbacks(t *testing.T) {
	useLAN(t, &lanNet{})
	st := testStorage(t)
	for name, body := range map[string]string{
		"config.json": `{"version":1,"bind":"127.0.0.9","discovery":true,"serial":"PRIVATE serial","email":"PRIVATE-not-an-email"}`,
		copyName:      `{"version":1,"files_at_once":57}`,
	} {
		if err := st.writeBytes(name, []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	var logs testlog.Buffer
	err := runUIPresented(t.Context(), st.root, io.Discard, &logs, nil, func(string) error { t.Error("opened browser"); return nil }, &fakeKeychain{}, func(_ context.Context, _ string, closeWindow func(), _ <-chan struct{}) error {
		closeWindow()
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[[2]string]string{
		{"config.json", "discovery"}: "off", {"config.json", "serial"}: "none", {"config.json", "email"}: "none",
		{copyName, "files_at_once"}: "4",
	}
	got := map[[2]string]string{}
	warnings := 0
	for line := range strings.Lines(logs.String()) {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil || record["msg"] != "settings_fallback" {
			continue
		}
		warnings++
		file, _ := record["file"].(string)
		setting, _ := record["setting"].(string)
		fallback, _ := record["fallback"].(string)
		if record["level"] != "WARN" {
			t.Fatalf("level = %v", record["level"])
		}
		got[[2]string{file, setting}] = fallback
	}
	if warnings != len(want) || len(got) != len(want) {
		t.Fatalf("warnings = %d %v; want %v; logs = %s", warnings, got, want, logs.String())
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%v fallback = %q; want %q", k, got[k], v)
		}
	}
	if strings.Contains(logs.String(), "PRIVATE") || strings.Contains(logs.String(), "127.0.0.9") {
		t.Fatalf("log names a saved value: %s", logs.String())
	}
}
