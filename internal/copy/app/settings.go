package app

import (
	"os"
	"path/filepath"
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
	IntervalMinutes int      `json:"interval_minutes"`
	Excludes        []string `json:"excludes"`
	Includes        []string `json:"includes"`
	SkipBuiltIn     bool     `json:"skip_built_in"`
	StartCopying    bool     `json:"start_copying"`
	FilesAtOnce     int      `json:"files_at_once"` // 1 to MaxFilesAtOnce
	// Once copies in one check, after which copying turns itself off;
	// otherwise copying checks again every IntervalMinutes.
	Once bool `json:"once"`
	// The Free up space options.
	OlderThanDays int  `json:"older_than_days"`
	TrustState    bool `json:"trust_state"`
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
	}
	if home, err := os.UserHomeDir(); err == nil {
		s.Folder = filepath.Join(home, "Seestar")
	}
	return s
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
