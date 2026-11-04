package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"
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
		Excludes: []string{}, Includes: []string{}, SkipBuiltIn: true, StartCopying: false, OlderThanDays: 7}
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
		Excludes: []string{}, Includes: []string{}, SkipBuiltIn: false, StartCopying: true, OlderThanDays: 7}
	if !f.exists || !reflect.DeepEqual(f.settings, want) {
		t.Errorf("got %+v, want %+v", f.settings, want)
	}
}

// Only a new settings file starts with copying at startup off. A file
// without start_copying keeps the earlier default, on, and a saved value
// is kept as it is.
func TestLoadStartCopying(t *testing.T) {
	for _, c := range []struct {
		member string
		want   bool
	}{{"", true}, {`,"start_copying":true`, true}, {`,"start_copying":false`, false}} {
		path := filepath.Join(t.TempDir(), "app.json")
		writeFile(t, path, `{"version":1,"address":"192.0.2.10:445","folder":"/data/Seestar"`+c.member+`}`)
		f, err := loadSettings(path)
		if err != nil {
			t.Fatal(err)
		}
		if f.settings.StartCopying != c.want {
			t.Errorf("file with %q: start_copying %v, want %v", c.member, f.settings.StartCopying, c.want)
		}
	}
}

// A null field keeps its default, as it did when app.json was read with
// encoding/json: v0.4.0 read these files so, and a zero interval would start
// each pass as the last one ended.
func TestLoadNullFieldsTakeDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	defaults := Settings{Version: 1, Folder: filepath.Join(home, "Seestar"), IntervalMinutes: 5,
		Excludes: []string{}, Includes: []string{}, SkipBuiltIn: true, StartCopying: true, OlderThanDays: 7}
	for _, tt := range []struct {
		name, data string
		want       Settings
	}{
		{"some", `{"version":1,"interval_minutes":null,"skip_built_in":null,"start_copying":true}`, defaults},
		{"all", `{"version":null,"address":null,"folder":null,"interval_minutes":null,"excludes":null,"includes":null,` +
			`"skip_built_in":null,"start_copying":null,"older_than_days":null,"trust_state":null}`, defaults},
		{"an element", `{"excludes":["Video",null]}`, func() Settings { s := defaults; s.Excludes = []string{"Video", ""}; return s }()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.json")
			writeFile(t, path, tt.data)
			f, err := loadSettings(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(f.settings, tt.want) {
				t.Errorf("got %+v, want %+v", f.settings, tt.want)
			}
		})
	}

	path := filepath.Join(t.TempDir(), "app.json")
	writeFile(t, path, `{"version":1,"address":"192.0.2.10:445","interval_minutes":null,"start_copying":true}`)
	f, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	e := newCopyEnv(t, fstest.MapFS{})
	if err := e.c.start(f.settings); err != nil {
		t.Fatal(err)
	}
	e.c.mu.Lock()
	interval := e.c.st.interval
	e.c.mu.Unlock()
	if interval != 5*time.Minute {
		t.Errorf("copying started with a pass every %v, want 5m", interval)
	}
}

// A repeated includes or excludes is read into the list before it, as
// encoding/json read it for v0.4.0: a null element keeps the string an
// earlier list had in its place. The expected lists are what v0.4.0 loaded.
// The selection they make is accepted, and a save keeps it.
func TestLoadRepeatedListsMerge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, tt := range []struct {
		data               string
		includes, excludes []string
	}{
		{`{"version":1,"includes":["Light"],"includes":[null]}`, []string{"Light"}, []string{}},
		{`{"version":1,"excludes":["Light"],"excludes":[null]}`, []string{}, []string{"Light"}},
		{`{"version":1,"includes":["a","b"],"includes":[null]}`, []string{"a"}, []string{}},
		{`{"version":1,"includes":["a","z"],"includes":[null,"b"]}`, []string{"a", "b"}, []string{}},
		{`{"version":1,"includes":["a","b"],"includes":["x"],"includes":[null,null]}`, []string{"x", "b"}, []string{}},
		{`{"version":1,"excludes":["a","b"],"excludes":["x"],"excludes":[null,null],"includes":["p"],"includes":[null]}`,
			[]string{"p"}, []string{"x", "b"}},
		{`{"version":1,"includes":["a"],"includes":[null],"includes":[]}`, []string{}, []string{}},
		{`{"version":1,"includes":["a"],"includes":null}`, []string{}, []string{}},
	} {
		t.Run(tt.data, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.json")
			writeFile(t, path, tt.data)
			f, err := loadSettings(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(f.settings.Includes, tt.includes) || !reflect.DeepEqual(f.settings.Excludes, tt.excludes) {
				t.Fatalf("includes %q, excludes %q; want %q, %q", f.settings.Includes, f.settings.Excludes, tt.includes, tt.excludes)
			}
			if _, err := selector(f.settings); err != nil {
				t.Errorf("selection refused: %v", err)
			}
			if err := f.save(f.settings); err != nil {
				t.Fatal(err)
			}
			again, err := loadSettings(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(again.settings, f.settings) {
				t.Errorf("reloaded %+v, want %+v", again.settings, f.settings)
			}
		})
	}

	// Where the earlier list had nothing in that place, a null element is
	// empty, as it was for v0.4.0, and the selection is refused.
	for _, tt := range []struct {
		data     string
		includes []string
	}{
		{`{"version":1,"includes":["a"],"includes":[],"includes":[null]}`, []string{""}},
		{`{"version":1,"includes":["a"],"includes":null,"includes":[null]}`, []string{""}},
		{`{"version":1,"includes":["a"],"includes":["b",null]}`, []string{"b", ""}},
		{`{"version":1,"includes":["a","b","c"],"includes":["x"],"includes":[null,null,null,null]}`, []string{"x", "b", "c", ""}},
	} {
		path := filepath.Join(t.TempDir(), "app.json")
		writeFile(t, path, tt.data)
		f, err := loadSettings(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(f.settings.Includes, tt.includes) {
			t.Errorf("%s: includes %q, want %q", tt.data, f.settings.Includes, tt.includes)
		}
		if _, err := selector(f.settings); err == nil {
			t.Errorf("%s: selection accepted", tt.data)
		}
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
		"from_a_later_version": map[string]any{"x": []any{1.0, 2.0}}, "older_than_days": 3.0, "trust_state": false,
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

// v0.4.0 kept an unknown key's value as it was, even one that encoding/json
// read but RFC 7493 does not allow: a duplicate name, an unpaired escaped
// surrogate or a byte that is not UTF-8. These files are as its save wrote
// them; they load, and a save writes them again byte for byte.
func TestSaveKeepsWhatEarlierVersionsKept(t *testing.T) {
	for _, tt := range []struct{ name, future string }{
		{"duplicate name", "{\n    \"member\": 1,\n    \"member\": 2\n  }"},
		{"unpaired surrogate", `"\ud800"`},
		{"invalid UTF-8", "\"bad\xff\""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			data := "{\n" +
				"  \"address\": \"\",\n" +
				"  \"excludes\": [],\n" +
				"  \"folder\": \"/data/synthetic\",\n" +
				"  \"future\": " + tt.future + ",\n" +
				"  \"includes\": [],\n" +
				"  \"interval_minutes\": 5,\n" +
				"  \"older_than_days\": 7,\n" +
				"  \"skip_built_in\": true,\n" +
				"  \"start_copying\": true,\n" +
				"  \"trust_state\": false,\n" +
				"  \"version\": 1\n" +
				"}\n"
			path := filepath.Join(t.TempDir(), "app.json")
			writeFile(t, path, data)
			f, err := loadSettings(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.save(f.settings); err != nil {
				t.Fatal(err)
			}
			if b, _ := os.ReadFile(path); string(b) != data {
				t.Errorf("saved\n%q\nwant\n%q", b, data)
			}
			again, err := loadSettings(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(again.settings, f.settings) {
				t.Errorf("reloaded %+v, want %+v", again.settings, f.settings)
			}
		})
	}
}

// A save writes what v0.4.0's encoding/json wrote: sorted keys, two-space
// indent, <, >, &, U+2028 and U+2029 escaped in known and kept values alike,
// a byte that is not UTF-8 as U+FFFD, and a nil list as null.
func TestSaveBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.json")
	writeFile(t, path, `{"version":1,"future":"x<y`+"\u2028"+`"}`)
	f, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	s := Settings{Address: "192.0.2.10:445", Folder: "/data/A<B>&C\u2028\u2029", IntervalMinutes: 5,
		Includes: []string{"a\xffb"}, SkipBuiltIn: true, OlderThanDays: 7}
	if err := f.save(s); err != nil {
		t.Fatal(err)
	}
	want := "{\n" +
		"  \"address\": \"192.0.2.10:445\",\n" +
		"  \"excludes\": null,\n" +
		"  \"folder\": \"/data/A\\u003cB\\u003e\\u0026C\\u2028\\u2029\",\n" +
		"  \"future\": \"x\\u003cy\\u2028\",\n" +
		"  \"includes\": [\n" +
		"    \"a\ufffdb\"\n" +
		"  ],\n" +
		"  \"interval_minutes\": 5,\n" +
		"  \"older_than_days\": 7,\n" +
		"  \"skip_built_in\": true,\n" +
		"  \"start_copying\": false,\n" +
		"  \"trust_state\": false,\n" +
		"  \"version\": 1\n" +
		"}\n"
	if b, _ := os.ReadFile(path); string(b) != want {
		t.Errorf("saved\n%q\nwant\n%q", b, want)
	}
}

// Keys are saved sorted, whatever order the file and the map held them in.
func TestSaveSortsKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.json")
	in, keys := `{"version":1`, ""
	for i := 19; i >= 0; i-- {
		in += fmt.Sprintf(`,"k%02d":%d`, i, i)
	}
	for i := range 20 {
		keys += fmt.Sprintf("  \"k%02d\": %d,\n", i, i)
	}
	writeFile(t, path, in+"}")
	f, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.save(Settings{Address: "192.0.2.10:445", Folder: "/data", IntervalMinutes: 5, OlderThanDays: 7}); err != nil {
		t.Fatal(err)
	}
	want := "{\n" +
		"  \"address\": \"192.0.2.10:445\",\n" +
		"  \"excludes\": null,\n" +
		"  \"folder\": \"/data\",\n" +
		"  \"includes\": null,\n" +
		"  \"interval_minutes\": 5,\n" +
		keys +
		"  \"older_than_days\": 7,\n" +
		"  \"skip_built_in\": false,\n" +
		"  \"start_copying\": false,\n" +
		"  \"trust_state\": false,\n" +
		"  \"version\": 1\n" +
		"}\n"
	if b, _ := os.ReadFile(path); string(b) != want {
		t.Errorf("saved\n%s\nwant\n%s", b, want)
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
		{"syntax", `{"address": `, `jsontext: unexpected EOF within "/address" after offset 12`},
		{"empty", ``, "jsontext: unexpected EOF"},
		{"array", `[1]`, "it is not a JSON object"},
		{"null", `null`, "it is not a JSON object"},
		{"version", `{"version":2,"address":"a:445"}`, "it has version 2, and this seestar-copy reads version 1"},
		{"version 0", `{"version":0}`, "it has version 0, and this seestar-copy reads version 1"},
		{"field type", `{"interval_minutes":"5"}`, `json: cannot unmarshal JSON string into Go int within "/interval_minutes"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.json")
			writeFile(t, path, tt.data)
			_, err := loadSettings(path)
			want := "Couldn't read settings from " + path + ": " + tt.reason + ". Fix or remove the file, then reopen seestar-copy."
			// encoding/json/v2 says "cannot" or "unable to", chosen at
			// random once per process.
			if err == nil || strings.Replace(err.Error(), "json: unable to ", "json: cannot ", 1) != want {
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
