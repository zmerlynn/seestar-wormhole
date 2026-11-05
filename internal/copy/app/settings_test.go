package app

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
		Excludes: []string{}, Includes: []string{}, SkipBuiltIn: true, StartCopying: false, OlderThanDays: 7}
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
