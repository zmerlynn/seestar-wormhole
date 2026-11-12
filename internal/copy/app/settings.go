package app

import (
	"os"
	"path/filepath"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

// SettingsVersion is the only version of the copy settings this app reads
// and writes.
const SettingsVersion = 1

// MaxFilesAtOnce is the most files a copy may run at once, and the default.
const MaxFilesAtOnce = 4

// Settings is the copy settings, as copy.json holds them: seestar-copy's
// app.json without the address, which is the [Target]'s.
type Settings struct {
	Version         int      `json:"version"`
	Address         string   `json:"-"` // the target's endpoint, set for each start
	Folder          string   `json:"folder"`
	HistoryDir      string   `json:"history_dir"` // absolute and outside Folder; "" is --history-dir's default
	IntervalMinutes int      `json:"interval_minutes"`
	Excludes        []string `json:"excludes"`
	Includes        []string `json:"includes"`
	SkipBuiltIn     bool     `json:"skip_built_in"`
	StartCopying    bool     `json:"start_copying"`
	FilesAtOnce     int      `json:"files_at_once"` // 1 to MaxFilesAtOnce
	// Once copies in one check, after which copying turns itself off;
	// otherwise copying checks again every IntervalMinutes.
	Once bool `json:"once"`
	// The Free up space options. Its patterns are its own: Excludes and
	// Includes select only what Copy copies.
	OlderThanDays int  `json:"older_than_days"`
	TrustState    bool `json:"trust_state"`
	// IgnoreState checks nothing: whatever is selected is deleted. A save
	// writes it or TrustState true, never both; on load it wins.
	IgnoreState  bool     `json:"ignore_state"`
	FreeExcludes []string `json:"free_excludes"` // Keep on the telescope
	FreeIncludes []string `json:"free_includes"` // Only free up
	// Telescopes are each telescope's folder, by its key: see SerialKey
	// and AddressKey. Folder stays the first telescope's, which an older
	// app reads. Until one has a folder here, Folder is every telescope's.
	Telescopes map[string]Telescope `json:"telescopes,omitempty"`
}

// DefaultSettings is what a missing file, or a field absent from one,
// means. The folder is ~/Seestar, or empty if there is no home folder.
func DefaultSettings() Settings {
	s := Settings{
		Version:         SettingsVersion,
		IntervalMinutes: 5,
		Excludes:        []string{},
		Includes:        []string{},
		SkipBuiltIn:     true,
		FilesAtOnce:     MaxFilesAtOnce,
		OlderThanDays:   7,
		FreeExcludes:    []string{},
		FreeIncludes:    []string{},
	}
	if home, err := os.UserHomeDir(); err == nil {
		s.Folder = filepath.Join(home, "Seestar")
	}
	return s
}

// firstSettings is what the app's first setup, with no settings saved,
// starts from: DefaultSettings with Only copy set to FITS files only, as
// the page's What to copy names them. A saved file's absent members still
// take DefaultSettings', so this changes no file.
func firstSettings() Settings {
	s := DefaultSettings()
	s.Includes = []string{"*.fit", "*.fits"}
	return s
}

// historyDir is the copy history folder s chooses, or def, the file
// commands' default, when it chooses none. A run that writes moves the old
// default folder there first, with [server.moveHistory].
func (s Settings) historyDir(def string) string {
	if s.HistoryDir != "" {
		return s.HistoryDir
	}
	return def
}

// historyToRead is the folder a read-only run reads for historyDir: the
// old default folder, in place, until a run that writes moves it.
func (s Settings) historyToRead(def string) string {
	return state.HistoryToRead(s.historyDir(def))
}

// settingsFile is the settings as last loaded or saved.
type settingsFile struct {
	settings Settings
	exists   bool
	// write keeps settings for the next start; nil keeps them in memory.
	write func(Settings) error
}

// save writes s, as the current version. On success it becomes the file's
// settings; on failure f is unchanged.
func (f *settingsFile) save(s Settings) error {
	s.Version, s.Address = SettingsVersion, ""
	if f.write != nil {
		if err := f.write(s); err != nil {
			return err
		}
	}
	f.settings, f.exists = s, true
	return nil
}
