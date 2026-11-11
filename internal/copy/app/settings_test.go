package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultSettings(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	t.Setenv("USERPROFILE", `C:\Users\someone`)
	home, _ := os.UserHomeDir()
	want := Settings{Version: 1, Folder: filepath.Join(home, "Seestar"), IntervalMinutes: 5,
		Excludes: []string{}, Includes: []string{}, SkipBuiltIn: true, StartCopying: false, FilesAtOnce: 4, OlderThanDays: 7,
		FreeExcludes: []string{}, FreeIncludes: []string{}}
	if got := DefaultSettings(); !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultSettings = %+v, want %+v", got, want)
	}
}

// A save hands the host the current version without the address, and
// takes effect only if the host kept it.
func TestSettingsFileSave(t *testing.T) {
	var got []Settings
	fail := errors.New("disk full")
	var err error
	f := &settingsFile{settings: DefaultSettings(), write: func(s Settings) error {
		if err != nil {
			return err
		}
		got = append(got, s)
		return nil
	}}
	s := DefaultSettings()
	s.Version, s.Address, s.Folder = 0, "192.0.2.10:445", abs("/data/Seestar")
	if e := f.save(s); e != nil {
		t.Fatal(e)
	}
	want := s
	want.Version, want.Address = SettingsVersion, ""
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) || !reflect.DeepEqual(f.settings, want) || !f.exists {
		t.Errorf("saved %+v, holds %+v (exists %v), want %+v", got, f.settings, f.exists, want)
	}
	err = fail
	other := want
	other.Folder = abs("/data/Other")
	if e := f.save(other); !errors.Is(e, fail) {
		t.Errorf("a failed write = %v", e)
	}
	if !reflect.DeepEqual(f.settings, want) {
		t.Errorf("a failed save changed the settings to %+v", f.settings)
	}
	// With no host to keep them, settings live in memory.
	mem := &settingsFile{settings: DefaultSettings()}
	if e := mem.save(want); e != nil || !mem.exists || !reflect.DeepEqual(mem.settings, want) {
		t.Errorf("an in-memory save = %v, %+v", e, mem.settings)
	}
}

// The first setup, with no settings saved, starts on FITS files only and
// saves the includes the page sends. Saved settings show as saved, an
// empty Only copy included, and DefaultSettings, which a saved file's
// absent members take, still copies everything.
func TestFirstSetupStartsOnFITSOnly(t *testing.T) {
	type status struct {
		SetUp    bool `json:"set_up"`
		Settings struct {
			Includes []string `json:"includes"`
		} `json:"settings"`
	}
	e := newAppEnv(t, nil)
	if v := decode[status](t, e.serve("GET", "/copy/api/status", "")); v.SetUp || !reflect.DeepEqual(v.Settings.Includes, []string{"*.fit", "*.fits"}) {
		t.Errorf("first setup = %+v; want FITS files only", v)
	}
	in := strings.Replace(goodInput, `"includes":[]`, `"includes":["*.fits","*.fit"]`, 1)
	if w := e.post("/copy/api/settings", in); w.Code != 200 {
		t.Fatalf("save = %d %s", w.Code, w.Body)
	}
	if s := e.saves(); len(s) != 1 || !reflect.DeepEqual(s[0].Includes, []string{"*.fits", "*.fit"}) {
		t.Errorf("saved %+v", s)
	}

	e = newAppEnv(t, savedAt(abs("/data/Seestar")))
	if v := decode[status](t, e.serve("GET", "/copy/api/status", "")); !v.SetUp || v.Settings.Includes == nil || len(v.Settings.Includes) != 0 {
		t.Errorf("saved with everything = %+v", v)
	}
	if got := DefaultSettings().Includes; len(got) != 0 {
		t.Errorf("DefaultSettings().Includes = %q; want none", got)
	}
}

// FITS files only selects a .fit or .fits file in any folder, in any case,
// and leaves out an ordinary JPG and a FITS file the built-ins exclude.
// A saved pattern keeps its spaces, so " *.fit", which the page shows as
// Custom, selects no .fit file.
func TestFITSOnlySelection(t *testing.T) {
	for _, tc := range []struct {
		includes []string
		path     string
		out      bool
	}{
		{firstSettings().Includes, "MyWorks/M 31/a.fit", false},
		{firstSettings().Includes, "a.FITS", false},
		{firstSettings().Includes, "MyWorks/M 31/a.jpg", true},
		{firstSettings().Includes, "MyWorks/notes.txt", true},
		{firstSettings().Includes, "log/hidden.fit", true},
		{[]string{" *.fit", "*.fits"}, "MyWorks/a.fit", true},
		{[]string{" *.fit", "*.fits"}, "MyWorks/a.FITS", false},
	} {
		set := firstSettings()
		set.Includes = tc.includes
		sel, err := selector(set)
		if err != nil {
			t.Fatal(err)
		}
		if got := sel.LeavesOut(tc.path, false); got != tc.out {
			t.Errorf("%q leaves out %s = %v; want %v", tc.includes, tc.path, got, tc.out)
		}
	}
}
