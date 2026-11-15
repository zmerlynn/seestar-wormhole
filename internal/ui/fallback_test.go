package ui

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"log/slog"
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
// naming the setting, its fallback and the saved value it replaced.
func checkFallback(t *testing.T, fallbacks []diagnostic.Fallback, file, setting, fallback, value string) {
	t.Helper()
	want := diagnostic.Fallback{File: file, Setting: setting, Fallback: fallback, Value: value}
	if len(fallbacks) != 1 || fallbacks[0] != want {
		t.Fatalf("fallbacks = %+v; want %+v", fallbacks, want)
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(fallbackLog(t, fallbacks)), &line); err != nil {
		t.Fatal(err)
	}
	if line["level"] != "WARN" || line["msg"] != "settings_fallback" || line["file"] != file || line["setting"] != setting || line["fallback"] != fallback || line["value"] != value {
		t.Fatalf("log line = %v", line)
	}
}

// A bad config.json value loads as its fallback and is logged with it.
func TestSettingsFallbacks(t *testing.T) {
	for _, tc := range []struct {
		setting, body, fallback, value string
		check                          func(Settings) bool
	}{
		{"discovery", `{"version":1,"bind":"127.0.0.9","discovery":true}`, "off", "127.0.0.9", func(s Settings) bool { return !s.Discovery && s.Bind == "127.0.0.9" }},
		{"serial", `{"version":1,"serial":"invalid serial\n"}`, "none", "invalid serial\n", func(s Settings) bool { return s.Serial == "" }},
		{"email", `{"version":1,"email":"synthetic-not-an-email"}`, "none", "synthetic-not-an-email", func(s Settings) bool { return s.Email == "" }},
	} {
		t.Run(tc.setting, func(t *testing.T) {
			st := testStorage(t)
			if err := st.writeBytes("config.json", []byte(tc.body)); err != nil {
				t.Fatal(err)
			}
			s, fallbacks, err := loadSettings(st)
			if err != nil || st.settingsRefused || !tc.check(s) {
				t.Fatalf("load = %+v, %v, refused %v", s, err, st.settingsRefused)
			}
			checkFallback(t, fallbacks, "config.json", tc.setting, tc.fallback, tc.value)
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
					bad.Email = "synthetic-not-an-email"
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
		`{"version":2,"email":"synthetic-not-an-email"}`, `{"email":"synthetic-not-an-email"}`, `{"version":1,"email":"synthetic-not-an-email"`, `{"version":1,"idle":120}`,
		`{"version":1,"idle":"soon"}`, `{"version":1,"idle":"-17m"}`, `{"version":1,"bind":"  "}`, `{"version":1,"model":"secret\nmodel"}`,
		`{"version":1,"ports":[{"local":80,"device":0}]}`,
	} {
		st := testStorage(t)
		if err := st.writeBytes("config.json", []byte(body)); err != nil {
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
		body, fallback, value string
		want                  int
	}{
		{`{"version":1,"folder":"/synthetic/Seestar","files_at_once":0}`, "1", "0", 1},
		{`{"version":1,"folder":"/synthetic/Seestar","files_at_once":-3}`, "1", "-3", 1},
		{`{"version":1,"folder":"/synthetic/Seestar","files_at_once":57}`, "4", "57", 4},
	} {
		t.Run(tc.body, func(t *testing.T) {
			st := testStorageWith(t, tc.body)
			_, set, fallbacks, err := loadCopySettings(st)
			if err != nil || set == nil || set.FilesAtOnce != tc.want {
				t.Fatalf("load = %+v, %v", set, err)
			}
			checkFallback(t, fallbacks, copyName, "files_at_once", tc.fallback, tc.value)
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

// The app logs each fallback in both files once in its log, with the
// saved value it replaced.
func TestAppLogsFallbacks(t *testing.T) {
	useLAN(t, &lanNet{})
	st := testStorage(t)
	for name, body := range map[string]string{
		"config.json": `{"version":1,"bind":"127.0.0.9","discovery":true,"serial":"INVALID serial","email":"SYNTHETIC-not-an-email"}`,
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
	values := map[[2]string]string{
		{"config.json", "discovery"}: "127.0.0.9", {"config.json", "serial"}: "INVALID serial", {"config.json", "email"}: "SYNTHETIC-not-an-email",
		{copyName, "files_at_once"}: "57",
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
		if value, _ := record["value"].(string); value != values[[2]string{file, setting}] {
			t.Errorf("%s value = %q", setting, value)
		}
	}
	if warnings != len(want) || len(got) != len(want) {
		t.Fatalf("warnings = %d %v; want %v; logs = %s", warnings, got, want, logs.String())
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%v fallback = %q; want %q", k, got[k], v)
		}
	}
}

// An oversized invalid saved email is logged clipped, with its prefix and a
// marker, in a record a log sink takes.
func TestSettingsFallbackClipsEmail(t *testing.T) {
	st := testStorage(t)
	email := "synthetic-" + strings.Repeat("<", 20000)
	if err := st.writeBytes("config.json", []byte(`{"version":1,"email":"`+email+`"}`)); err != nil {
		t.Fatal(err)
	}
	s, fallbacks, err := loadSettings(st)
	if err != nil || s.Email != "" || len(fallbacks) != 1 {
		t.Fatalf("load = %+v, %v, %v", s, fallbacks, err)
	}
	log := fallbackLog(t, fallbacks)
	var record struct{ Value string }
	if err := json.Unmarshal([]byte(log), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	if len(log) >= 16<<10 || !strings.HasPrefix(record.Value, "synthetic-<<<") || !strings.HasSuffix(record.Value, "…(truncated, 20010 bytes)") {
		t.Fatalf("record of %d bytes: %.300s", len(log), log)
	}
}
