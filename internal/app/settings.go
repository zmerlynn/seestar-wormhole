package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zmerlynn/seestar-copy/internal/readonly"
)

// settingsVersion is the only version of app.json this app reads and writes.
const settingsVersion = 1

// Settings is one window's setup, as app.json holds it.
type Settings struct {
	Version         int      `json:"version"`
	Address         string   `json:"address"`
	Folder          string   `json:"folder"`
	IntervalMinutes int      `json:"interval_minutes"`
	Excludes        []string `json:"excludes"`
	Includes        []string `json:"includes"`
	SkipBuiltIn     bool     `json:"skip_built_in"`
	StartCopying    bool     `json:"start_copying"`
}

// defaultSettings is what a missing file, or a field absent from one,
// means. The folder is ~/Seestar, or empty if there is no home folder.
func defaultSettings() Settings {
	s := Settings{
		Version:         settingsVersion,
		IntervalMinutes: 5,
		Excludes:        []string{},
		Includes:        []string{},
		SkipBuiltIn:     true,
		StartCopying:    true,
	}
	if home, err := os.UserHomeDir(); err == nil {
		s.Folder = filepath.Join(home, "Seestar")
	}
	return s
}

// settingsFile is app.json as last read or saved: the known fields, and
// every key as it was in the file, so that a save keeps the keys this
// version does not know.
type settingsFile struct {
	path     string
	settings Settings
	raw      map[string]json.RawMessage
	exists   bool
}

// loadError is a settings file that can't be read. Its text is the startup
// error the app shows before it exits.
type loadError struct {
	path   string
	reason string
}

func (e *loadError) Error() string {
	return fmt.Sprintf("Couldn't read settings from %s: %s. Fix or remove the file, then reopen seestar-copy.", e.path, e.reason)
}

// loadSettings reads path. A missing file is the defaults; a file that
// does not parse, or has another version, is a *loadError and is left as
// it is.
func loadSettings(path string) (*settingsFile, error) {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	f := &settingsFile{path: path, settings: defaultSettings(), raw: map[string]json.RawMessage{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return nil, &loadError{path, err.Error()}
	}
	err = json.Unmarshal(b, &f.raw)
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) || err == nil && f.raw == nil {
		// An array, a string, a number or null.
		return nil, &loadError{path, "it is not a JSON object"}
	}
	if err != nil {
		return nil, &loadError{path, err.Error()}
	}
	if err := json.Unmarshal(b, &f.settings); err != nil {
		return nil, &loadError{path, err.Error()}
	}
	if _, ok := f.raw["version"]; ok && f.settings.Version != settingsVersion {
		return nil, &loadError{path, fmt.Sprintf("it has version %d, and this seestar-copy reads version %d", f.settings.Version, settingsVersion)}
	}
	f.settings.Version = settingsVersion
	if f.settings.Excludes == nil {
		f.settings.Excludes = []string{}
	}
	if f.settings.Includes == nil {
		f.settings.Includes = []string{}
	}
	f.exists = true
	return f, nil
}

// save writes s to the file, keeping the keys it does not know. On success
// it becomes the file's settings; on failure the file and f are unchanged.
func (f *settingsFile) save(s Settings) error {
	s.Version = settingsVersion
	known, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(known, &fields); err != nil {
		return err
	}
	raw := make(map[string]json.RawMessage, len(f.raw)+len(fields))
	for k, v := range f.raw {
		raw[k] = v
	}
	for k, v := range fields {
		raw[k] = v
	}
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(f.path, append(b, '\n')); err != nil {
		return err
	}
	f.settings, f.raw, f.exists = s, raw, true
	return nil
}

// writeFileAtomic replaces path with b: a temp file in the same folder,
// written and synced, then renamed over path. A failure leaves path as it
// was and removes the temp file.
func writeFileAtomic(path string, b []byte) (err error) {
	readonly.Check("write settings")
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".app.json-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(b); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
