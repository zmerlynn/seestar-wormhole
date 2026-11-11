package ui

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// A copy.json from before Files at once loads as 4.
func TestCopySettingsVersionOneFixture(t *testing.T) {
	st := testStorage(t)
	body := []byte(`{
  "version":1,"folder":"/synthetic/Seestar","interval_minutes":15,
  "excludes":["Video","/Preview"],"includes":["MyWorks"],
  "skip_built_in":false,"start_copying":true,
  "older_than_days":30,"trust_state":true
}`)
	if err := st.writeBytes(copyName, body); err != nil {
		t.Fatal(err)
	}
	_, got, _, err := loadCopySettings(st)
	if err != nil || got == nil {
		t.Fatalf("load = %v, %v", got, err)
	}
	want := copyapp.Settings{Version: 1, Folder: "/synthetic/Seestar", IntervalMinutes: 15, Excludes: []string{"Video", "/Preview"},
		Includes: []string{"MyWorks"}, StartCopying: true, FilesAtOnce: 4, OlderThanDays: 30, TrustState: true,
		FreeExcludes: []string{}, FreeIncludes: []string{}}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("settings = %+v; want %+v", *got, want)
	}
	after, err := os.ReadFile(filepath.Join(st.root, copyName))
	if err != nil || !bytes.Equal(after, body) {
		t.Fatalf("load rewrote copy.json: %s, %v", after, err)
	}
}

// Copy once loads as saved, and a save keeps it; a file without it checks
// again every interval, as before.
func TestCopySettingsOnce(t *testing.T) {
	st := testStorage(t)
	if err := st.writeBytes(copyName, []byte(`{"version":1,"once":true}`)); err != nil {
		t.Fatal(err)
	}
	c, got, _, err := loadCopySettings(st)
	if err != nil || got == nil || !got.Once {
		t.Fatalf("once = %+v, %v", got, err)
	}
	got.Once = false
	if err := c.save(*got); err != nil {
		t.Fatal(err)
	}
	if _, again, _, err := loadCopySettings(st); err != nil || again == nil || again.Once {
		t.Errorf("after saving it off = %+v, %v", again, err)
	}
}

// Files at once loads from 1 to 4 as saved.
func TestCopySettingsFilesAtOnce(t *testing.T) {
	st := testStorage(t)
	for n := 1; n <= 4; n++ {
		if err := st.writeBytes(copyName, []byte(`{"version":1,"files_at_once":`+strconv.Itoa(n)+`}`)); err != nil {
			t.Fatal(err)
		}
		if _, got, _, err := loadCopySettings(st); err != nil || got == nil || got.FilesAtOnce != n {
			t.Errorf("files_at_once %d = %+v, %v", n, got, err)
		}
	}
}

func TestCopySettingsAbsentMembersDefault(t *testing.T) {
	st := testStorage(t)
	for _, body := range []string{`{"version":1}`, `{"version":1,"excludes":null,"includes":null}`} {
		if err := st.writeBytes(copyName, []byte(body)); err != nil {
			t.Fatal(err)
		}
		_, got, _, err := loadCopySettings(st)
		if want := copyapp.DefaultSettings(); err != nil || got == nil || !reflect.DeepEqual(*got, want) {
			t.Errorf("%s = %+v, %v; want %+v", body, got, err, want)
		}
	}
}

// Free up space's patterns load as saved and survive a save; a file
// without them, or with them null, has both empty.
func TestCopySettingsFreePatterns(t *testing.T) {
	st := testStorage(t)
	for _, body := range []string{`{"version":1,"excludes":["Video"]}`, `{"version":1,"excludes":["Video"],"free_excludes":null,"free_includes":null}`} {
		if err := st.writeBytes(copyName, []byte(body)); err != nil {
			t.Fatal(err)
		}
		if _, got, _, err := loadCopySettings(st); err != nil || got == nil || got.FreeExcludes == nil || len(got.FreeExcludes) != 0 || got.FreeIncludes == nil || len(got.FreeIncludes) != 0 {
			t.Errorf("%s = %+v, %v", body, got, err)
		}
	}
	body := `{"version":1,"excludes":["Video"],"free_excludes":["/MyWorks/M 31","*.mp4"],"free_includes":["/MyWorks"],"future_flag":true}`
	if err := st.writeBytes(copyName, []byte(body)); err != nil {
		t.Fatal(err)
	}
	c, set, _, err := loadCopySettings(st)
	if err != nil || set == nil {
		t.Fatalf("load = %+v, %v", set, err)
	}
	if !reflect.DeepEqual(set.FreeExcludes, []string{"/MyWorks/M 31", "*.mp4"}) || !reflect.DeepEqual(set.FreeIncludes, []string{"/MyWorks"}) ||
		!reflect.DeepEqual(set.Excludes, []string{"Video"}) {
		t.Errorf("loaded %+v", set)
	}
	set.FreeIncludes = []string{}
	if err := c.save(*set); err != nil {
		t.Fatal(err)
	}
	_, again, _, err := loadCopySettings(st)
	if err != nil || again == nil || !reflect.DeepEqual(again.FreeExcludes, []string{"/MyWorks/M 31", "*.mp4"}) || len(again.FreeIncludes) != 0 {
		t.Errorf("after a save = %+v, %v", again, err)
	}
	b, err := os.ReadFile(filepath.Join(st.root, copyName))
	if err != nil || !bytes.Contains(b, []byte(`"future_flag":true`)) || !bytes.Contains(b, []byte(`"version":1,`)) {
		t.Errorf("copy.json = %s, %v", b, err)
	}
}

// No copy.json is no settings yet, and loading doesn't create one.
func TestCopySettingsMissing(t *testing.T) {
	st := testStorage(t)
	c, got, _, err := loadCopySettings(st)
	if err != nil || got != nil || c == nil {
		t.Fatalf("load = %v, %+v, %v", c, got, err)
	}
	if _, err := os.Lstat(filepath.Join(st.root, copyName)); !os.IsNotExist(err) {
		t.Errorf("load created copy.json: %v", err)
	}
	set := copyapp.DefaultSettings()
	set.Folder = "/synthetic/Seestar"
	if err := c.save(set); err != nil {
		t.Fatal(err)
	}
	if _, again, _, err := loadCopySettings(st); err != nil || again == nil || !reflect.DeepEqual(*again, set) {
		t.Errorf("after a save = %+v, %v", again, err)
	}
}

// A save keeps the members this version doesn't know, and writes no
// address: it is the route's.
func TestCopySettingsUnknownMembersSurviveSave(t *testing.T) {
	st := testStorage(t)
	body := `{"version":1,"folder":"/synthetic/Seestar","future_object":{"enabled":true,"limit":17},"future_array":[1,"two",{"three":null}],"future_flag":false}`
	if err := st.writeBytes(copyName, []byte(body)); err != nil {
		t.Fatal(err)
	}
	c, set, _, err := loadCopySettings(st)
	if err != nil {
		t.Fatal(err)
	}
	set.Folder, set.IntervalMinutes, set.FilesAtOnce, set.Address = "/synthetic/Other", 9, 1, "192.0.2.10:445"
	if err := c.save(*set); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(st.root, copyName))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"version": 1.0, "folder": "/synthetic/Other", "history_dir": "", "interval_minutes": 9.0, "excludes": []any{}, "includes": []any{},
		"skip_built_in": true, "start_copying": false, "once": false, "files_at_once": 1.0, "older_than_days": 7.0, "trust_state": false,
		"free_excludes": []any{}, "free_includes": []any{},
		"future_object": map[string]any{"enabled": true, "limit": 17.0}, "future_array": []any{1.0, "two", map[string]any{"three": nil}}, "future_flag": false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("copy.json = %s", b)
	}
}

// refusedBodies are copy.json bodies this version refuses.
var refusedBodies = []string{`{"version":2,"future":{"keep":[1,2]}}`, `{"version":1,"interval_minutes":"5"}`, `[1]`, `{"version":1`,
	`{}`, `{"folder":"/synthetic/Seestar"}`, `null`,
	`{"version":1,"files_at_once":2.5}`,
	`{"version":1,"history_dir":"synthetic/history"}`, `{"version":1,"history_dir":7}`}

// A chosen copy history folder loads as saved and survives a save.
func TestCopySettingsHistoryDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	b, _ := json.Marshal(dir)
	st := testStorageWith(t, `{"version":1,"folder":"/synthetic/Seestar","history_dir":`+string(b)+`}`)
	c, set, _, err := loadCopySettings(st)
	if err != nil || set == nil || set.HistoryDir != dir {
		t.Fatalf("load = %+v, %v; want history_dir %s", set, err, dir)
	}
	if err := c.save(*set); err != nil {
		t.Fatal(err)
	}
	if _, again, _, err := loadCopySettings(st); err != nil || again == nil || again.HistoryDir != dir {
		t.Errorf("after a save = %+v, %v", again, err)
	}
}

// v51Settings and v51File are copy.json's types as 92c76d99 (#51) had
// them, before history_dir, frozen here: an app of that version keeps the
// member through a load and a save, so this version reads it back.
type v51Settings struct {
	Version         int      `json:"version"`
	Address         string   `json:"-"`
	Folder          string   `json:"folder"`
	IntervalMinutes int      `json:"interval_minutes"`
	Excludes        []string `json:"excludes"`
	Includes        []string `json:"includes"`
	SkipBuiltIn     bool     `json:"skip_built_in"`
	StartCopying    bool     `json:"start_copying"`
	FilesAtOnce     int      `json:"files_at_once"`
	Once            bool     `json:"once"`
	OlderThanDays   int      `json:"older_than_days"`
	TrustState      bool     `json:"trust_state"`
	FreeExcludes    []string `json:"free_excludes"`
	FreeIncludes    []string `json:"free_includes"`
}

type v51File struct {
	v51Settings
	Unknown jsontext.Value `json:",embed"`
}

func TestCopySettingsHistoryDirSurvivesOlderApp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	st := testStorage(t)
	c, _, _, err := loadCopySettings(st)
	if err != nil {
		t.Fatal(err)
	}
	set := copyapp.DefaultSettings()
	set.Folder, set.HistoryDir = "/synthetic/Seestar", dir
	if err := c.save(set); err != nil {
		t.Fatal(err)
	}
	// The older app loads the file, changes a setting and saves it.
	var old v51File
	if err := st.read(copyName, &old); err != nil {
		t.Fatal(err)
	}
	old.IntervalMinutes = 9
	if err := st.write(copyName, old); err != nil {
		t.Fatal(err)
	}
	_, got, _, err := loadCopySettings(st)
	if err != nil || got == nil || got.HistoryDir != dir || got.IntervalMinutes != 9 {
		t.Errorf("after the older app's save = %+v, %v; want history_dir %s, interval 9", got, err, dir)
	}
}

// testStorageWith is a state dir whose copy.json is body.
func testStorageWith(t *testing.T, body string) *storage {
	st := testStorage(t)
	if err := st.writeBytes(copyName, []byte(body)); err != nil {
		t.Fatal(err)
	}
	return st
}

// refusedStart runs the UI over a state dir whose copy.json is body, with
// drive as its window, which gets a client, the window's quit and the
// state dir, and returns the state dir once the UI has stopped. It checks
// the UI opened and copy.json was left as it was.
func refusedStart(t *testing.T, ctx context.Context, body string, drive func(c *runtimeClient, quit func(), root string)) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	st, err := openStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	err = st.writeBytes(copyName, []byte(body))
	st.directory.Close()
	if err != nil {
		t.Fatal(err)
	}
	opened := false
	window := func(ctx context.Context, page string, quit func(), _ <-chan struct{}) error {
		opened = true
		drive(newRuntimeClient(t, page), quit, root)
		<-ctx.Done()
		return nil
	}
	finished := make(chan error, 1)
	go func() {
		finished <- runUIPresented(ctx, root, io.Discard, io.Discard, func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status, _ func(proxy.Dialer) (func(), bool)) error {
			<-ctx.Done()
			return nil
		}, func(string) error {
			t.Error("opened a browser")
			return nil
		}, &fakeKeychain{}, window, &copyapp.Native{})
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("runUIPresented = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the UI didn't stop")
	}
	if !opened {
		t.Error("the UI didn't open")
	}
	after, err := os.ReadFile(filepath.Join(root, copyName))
	if err != nil || string(after) != body {
		t.Errorf("refused file changed: %s, %v", after, err)
	}
	return root
}

// A copy.json this version can't read leaves the app opening, with Copy,
// Browse and Free up space off, the page told so and where the file is,
// Activity given its fixed line, and the file left as it was.
func TestCopySettingsRefusedOpensWithoutCopy(t *testing.T) {
	for _, body := range refusedBodies {
		t.Run(body, func(t *testing.T) {
			if _, _, _, err := loadCopySettings(testStorageWith(t, body)); err == nil {
				t.Fatal("load accepted it")
			}
			refusedStart(t, t.Context(), body, func(c *runtimeClient, quit func(), root string) {
				defer quit()
				code, reply := c.do("GET", "/api/status", "")
				var status struct {
					CopyRefused  *bool `json:"copy_refused"`
					CopyUnlocked bool  `json:"copy_unlocked"`
					Files        struct {
						Copy string `json:"copy"`
					} `json:"files"`
					Events []struct {
						Message string `json:"message"`
					} `json:"events"`
				}
				if err := json.Unmarshal([]byte(reply), &status); code != 200 || err != nil {
					t.Fatalf("status = %d %s: %v", code, reply, err)
				}
				if status.CopyRefused == nil || !*status.CopyRefused || status.CopyUnlocked {
					t.Errorf("status = %s; want copy_refused true and copy_unlocked false", reply)
				}
				if abs, _ := filepath.Abs(filepath.Join(root, copyName)); status.Files.Copy != abs {
					t.Errorf("files.copy = %q; want %q", status.Files.Copy, abs)
				}
				found := false
				for _, e := range status.Events {
					found = found || e.Message == copyRefusedEvent
				}
				if !found {
					t.Errorf("events = %s; want %q", reply, copyRefusedEvent)
				}
				for _, path := range []string{"/copy/api/status", "/browse/", "/browse/api/list?path=MyWorks"} {
					if code, _ := c.do("GET", path, ""); code != 404 {
						t.Errorf("GET %s = %d; want 404", path, code)
					}
				}
			})
		})
	}
}

// A refused copy.json is never written, by any request, and no Mount is
// made for the run.
func TestCopySettingsRefusedNeverWrites(t *testing.T) {
	saved, savedMount := writeCopySettings, newMount
	t.Cleanup(func() { writeCopySettings, newMount = saved, savedMount })
	writeCopySettings = func(*storage, copyFile) error {
		t.Error("copy.json was written")
		return errStorage
	}
	var mounts atomic.Int32
	newMount = func(ctx context.Context, o copyapp.MountOptions) *copyapp.Mount {
		mounts.Add(1)
		return savedMount(ctx, o)
	}
	refusedStart(t, t.Context(), `{"version":2,"folder":"/synthetic/Seestar"}`, func(c *runtimeClient, _ func(), _ string) {
		body := `{"settings":{"route":"lan","lan_address":"192.0.2.10:445"}}`
		if code, reply := c.do("POST", "/api/route", body); code != 200 {
			t.Errorf("Connection Save = %d %s", code, reply)
		}
		for _, post := range []struct{ path, body string }{
			{"/copy/api/settings", `{"folder":"/synthetic/Other"}`},
			{"/copy/api/delete", `{"action":"find","days":"0"}`},
		} {
			if code, _ := c.do("POST", post.path, post.body); code != 404 {
				t.Errorf("POST %s = %d; want 404", post.path, code)
			}
		}
		if code, reply := c.do("POST", "/api/quit", "{}"); code != 200 {
			t.Errorf("quit = %d %s", code, reply)
		}
	})
	if n := mounts.Load(); n != 0 {
		t.Errorf("Mount was made %d times for a refused copy.json", n)
	}
}

// A refused start quits cleanly by the window and by the end of its
// context, with no copier to stop.
func TestCopySettingsRefusedQuits(t *testing.T) {
	t.Run("window", func(t *testing.T) {
		refusedStart(t, t.Context(), `{"version":2}`, func(_ *runtimeClient, quit func(), _ string) { quit() })
	})
	t.Run("context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		refusedStart(t, ctx, `{"version":2}`, func(*runtimeClient, func(), string) { cancel() })
	})
}

// LoadCopySettings, the file commands' reader, loads copy.json as the app
// does and never writes it: a missing file or folder is
// ErrNoCopySettings, and every body the app refuses is an error.
func TestLoadCopySettings(t *testing.T) {
	saved := writeCopySettings
	t.Cleanup(func() { writeCopySettings = saved })
	writeCopySettings = func(*storage, copyFile) error {
		t.Error("copy.json was written")
		return errStorage
	}
	// The history folder must be absolute on this system: on Windows a
	// path needs its drive.
	history := filepath.Join(t.TempDir(), "history")
	quoted, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	st := testStorageWith(t, `{"version":1,"folder":"/synthetic/Seestar","history_dir":`+string(quoted)+`,"future":true}`)
	set, _, err := LoadCopySettings(st.root)
	if err != nil || set.Folder != "/synthetic/Seestar" || set.HistoryDir != history || set.FilesAtOnce != copyapp.MaxFilesAtOnce || !set.SkipBuiltIn {
		t.Errorf("= %+v, %v", set, err)
	}
	for _, body := range refusedBodies {
		if _, _, err := LoadCopySettings(testStorageWith(t, body).root); err == nil || errors.Is(err, ErrNoCopySettings) {
			t.Errorf("%s: %v; want a refusal", body, err)
		}
	}
	if _, _, err := LoadCopySettings(testStorage(t).root); !errors.Is(err, ErrNoCopySettings) {
		t.Errorf("no copy.json: %v", err)
	}
	if _, _, err := LoadCopySettings(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrNoCopySettings) {
		t.Errorf("no state folder: %v", err)
	}
}
