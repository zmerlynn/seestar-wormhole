package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingIsDefaults(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	t.Setenv("USERPROFILE", `C:\Users\someone`)
	path := filepath.Join(t.TempDir(), "app.json")
	f, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	want := Settings{Version: 1, Folder: filepath.Join(home, "Seestar"), IntervalMinutes: 5,
		Excludes: []string{}, Includes: []string{}, SkipBuiltIn: true, StartCopying: true}
	if f.exists || !reflect.DeepEqual(f.settings, want) {
		t.Errorf("missing file = %+v (exists %v), want %+v", f.settings, f.exists, want)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("loading created the file: %v", err)
	}
}

func TestLoadAbsentFieldsTakeDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.json")
	writeFile(t, path, `{"address":"192.0.2.10:445","folder":"/data/Seestar","skip_built_in":false}`)
	f, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{Version: 1, Address: "192.0.2.10:445", Folder: "/data/Seestar", IntervalMinutes: 5,
		Excludes: []string{}, Includes: []string{}, SkipBuiltIn: false, StartCopying: true}
	if !f.exists || !reflect.DeepEqual(f.settings, want) {
		t.Errorf("got %+v, want %+v", f.settings, want)
	}
}

func TestSaveRoundTripKeepsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.json")
	writeFile(t, path, `{"version":1,"address":"a:445","from_a_later_version":{"x":[1,2]},"older_than_days":3}`)
	f, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	s := f.settings
	s.Address, s.Folder, s.IntervalMinutes = "192.0.2.10:445", "/x/y", 30
	s.Excludes, s.Includes, s.SkipBuiltIn, s.StartCopying = []string{"Video", "/Preview"}, []string{"MyWorks"}, false, false
	if err := f.save(s); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("saved file does not parse: %v\n%s", err, b)
	}
	want := map[string]any{
		"version": 1.0, "address": "192.0.2.10:445", "folder": "/x/y", "interval_minutes": 30.0,
		"excludes": []any{"Video", "/Preview"}, "includes": []any{"MyWorks"},
		"skip_built_in": false, "start_copying": false,
		"from_a_later_version": map[string]any{"x": []any{1.0, 2.0}}, "older_than_days": 3.0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("saved\n%s\nwant %v", b, want)
	}
	again, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.settings, s) {
		t.Errorf("reloaded %+v, want %+v", again.settings, s)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("the folder holds %d entries after a save, want only app.json", len(entries))
	}
}

func TestSaveCreatesFolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "seestar-copy", "app.json")
	f, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.save(f.settings); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSettings(path); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRefusesAndLeavesFile(t *testing.T) {
	for _, tt := range []struct{ name, data, reason string }{
		{"syntax", `{"address": `, "unexpected end of JSON input"},
		{"empty", ``, "unexpected end of JSON input"},
		{"array", `[1]`, "it is not a JSON object"},
		{"null", `null`, "it is not a JSON object"},
		{"version", `{"version":2,"address":"a:445"}`, "it has version 2, and this seestar-copy reads version 1"},
		{"version 0", `{"version":0}`, "it has version 0, and this seestar-copy reads version 1"},
		{"field type", `{"interval_minutes":"5"}`, "json: cannot unmarshal string into Go struct field Settings.interval_minutes of type int"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.json")
			writeFile(t, path, tt.data)
			_, err := loadSettings(path)
			want := "Couldn't read settings from " + path + ": " + tt.reason + ". Fix or remove the file, then reopen seestar-copy."
			if err == nil || err.Error() != want {
				t.Fatalf("err = %v\nwant %s", err, want)
			}
			if b, _ := os.ReadFile(path); string(b) != tt.data {
				t.Errorf("the file changed to %q", b)
			}
		})
	}
}

func TestLoadRelativePathIsShownAbsolute(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, "app.json", `{`)
	_, err := loadSettings("app.json")
	if err == nil || !strings.HasPrefix(err.Error(), "Couldn't read settings from "+filepath.Join(dir, "app.json")+": ") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadUnreadable(t *testing.T) {
	// A folder where the file should be cannot be read as one, on every
	// OS and as any user.
	path := filepath.Join(t.TempDir(), "app.json")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := loadSettings(path)
	if err == nil || !strings.HasPrefix(err.Error(), "Couldn't read settings from "+path+": ") || strings.Contains(err.Error(), "read "+path) {
		t.Errorf("err = %v; want the startup message with the reason alone", err)
	}
}

func TestSaveFailureKeepsFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only folder does not stop file creation on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root writes into a read-only folder")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "app.json")
	const old = `{"version":1,"address":"a:445"}`
	writeFile(t, path, old)
	f, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	s := f.settings
	s.Address = "b:445"
	if err := f.save(s); err == nil {
		t.Fatal("save into a read-only folder succeeded")
	}
	if b, _ := os.ReadFile(path); string(b) != old {
		t.Errorf("the file changed to %q", b)
	}
	if f.settings.Address != "a:445" {
		t.Errorf("a failed save changed the settings to %+v", f.settings)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("the folder holds %d entries, want only app.json", len(entries))
	}
}

func TestSaveRenameFailureRemovesTemp(t *testing.T) {
	// The target is a non-empty folder, so the rename fails after the temp
	// file is written.
	dir := t.TempDir()
	path := filepath.Join(dir, "app.json")
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &settingsFile{path: path, settings: defaultSettings(), raw: map[string]json.RawMessage{}}
	if err := f.save(f.settings); err == nil {
		t.Fatal("save over a folder succeeded")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "app.json" {
		t.Errorf("the folder holds %v, want only app.json", entries)
	}
	if f.exists {
		t.Error("a failed save marked the file as existing")
	}
}
