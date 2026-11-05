package ui

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
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
	_, got, err := loadCopySettings(st)
	if err != nil || got == nil {
		t.Fatalf("load = %v, %v", got, err)
	}
	want := copyapp.Settings{Version: 1, Folder: "/synthetic/Seestar", IntervalMinutes: 15, Excludes: []string{"Video", "/Preview"},
		Includes: []string{"MyWorks"}, StartCopying: true, FilesAtOnce: 4, OlderThanDays: 30, TrustState: true}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("settings = %+v; want %+v", *got, want)
	}
	after, err := os.ReadFile(filepath.Join(st.root, copyName))
	if err != nil || !bytes.Equal(after, body) {
		t.Fatalf("load rewrote copy.json: %s, %v", after, err)
	}
}

// Files at once loads from 1 to 4 as saved.
func TestCopySettingsFilesAtOnce(t *testing.T) {
	st := testStorage(t)
	for n := 1; n <= 4; n++ {
		if err := st.writeBytes(copyName, []byte(`{"version":1,"files_at_once":`+strconv.Itoa(n)+`}`)); err != nil {
			t.Fatal(err)
		}
		if _, got, err := loadCopySettings(st); err != nil || got == nil || got.FilesAtOnce != n {
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
		_, got, err := loadCopySettings(st)
		if want := copyapp.DefaultSettings(); err != nil || got == nil || !reflect.DeepEqual(*got, want) {
			t.Errorf("%s = %+v, %v; want %+v", body, got, err, want)
		}
	}
}

// No copy.json is no settings yet, and loading doesn't create one.
func TestCopySettingsMissing(t *testing.T) {
	st := testStorage(t)
	c, got, err := loadCopySettings(st)
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
	if _, again, err := loadCopySettings(st); err != nil || again == nil || !reflect.DeepEqual(*again, set) {
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
	c, set, err := loadCopySettings(st)
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
		"version": 1.0, "folder": "/synthetic/Other", "interval_minutes": 9.0, "excludes": []any{}, "includes": []any{},
		"skip_built_in": true, "start_copying": false, "files_at_once": 1.0, "older_than_days": 7.0, "trust_state": false,
		"future_object": map[string]any{"enabled": true, "limit": 17.0}, "future_array": []any{1.0, "two", map[string]any{"three": nil}}, "future_flag": false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("copy.json = %s", b)
	}
}

// A copy.json this version can't read stops the app at startup, and is
// left as it was.
func TestCopySettingsRefusedStopsStartup(t *testing.T) {
	for _, body := range []string{`{"version":2,"future":{"keep":[1,2]}}`, `{"version":1,"interval_minutes":"5"}`, `[1]`, `{"version":1`,
		`{}`, `{"folder":"/synthetic/Seestar"}`, `null`,
		`{"version":1,"files_at_once":0}`, `{"version":1,"files_at_once":5}`, `{"version":1,"files_at_once":-1}`, `{"version":1,"files_at_once":2.5}`} {
		t.Run(body, func(t *testing.T) {
			s := testServer(t)
			if err := s.storage.writeBytes(copyName, []byte(body)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := loadCopySettings(s.storage); err == nil {
				t.Fatal("load accepted it")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			t.Chdir(filepath.Dir(s.storage.root))
			err := runUIPresented(ctx, filepath.Base(s.storage.root), io.Discard, io.Discard, nil, func(string) error {
				t.Error("opened UI for refused copy settings")
				return nil
			}, &fakeKeychain{}, nil, nil)
			if err == nil || err.Error() != "Could not load saved settings safely.\n\n"+filepath.Join(s.storage.root, copyName)+"\n\nCheck this file, then reopen seestar-wormhole." {
				t.Errorf("startup did not explain refusal: %v", err)
			}
			after, err := os.ReadFile(filepath.Join(s.storage.root, copyName))
			if err != nil || string(after) != body {
				t.Fatalf("refused file changed: %s, %v", after, err)
			}
		})
	}
}
