package ui

import (
	"encoding/json/jsontext"
	"os"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
)

// copyName is the copy settings' file in the state dir.
const copyName = "copy.json"

// copyFile is copy.json: seestar-copy's app.json without the address,
// which is the route's, and the members a later version added, which a
// save keeps.
type copyFile struct {
	copyapp.Settings
	Unknown jsontext.Value `json:",embed"`
}

// copyStore is copy.json as loaded at startup and kept by each save.
type copyStore struct {
	storage *storage
	unknown jsontext.Value
}

// loadCopySettings reads copy.json without writing it. A missing file is
// nil settings, which the Copy tab asks for; a file that doesn't parse or
// has another version or none is an error, and the app doesn't start.
func loadCopySettings(st *storage) (*copyStore, *copyapp.Settings, error) {
	c := &copyStore{storage: st}
	// Absent members take their defaults, except the version, which the
	// file must name; unknown ones are kept.
	f := copyFile{Settings: copyapp.DefaultSettings()}
	f.Version = 0
	if err := st.read(copyName, &f); os.IsNotExist(err) {
		return c, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	if f.Version != copyapp.SettingsVersion || f.FilesAtOnce < 1 || f.FilesAtOnce > copyapp.MaxFilesAtOnce {
		return nil, nil, errStorage
	}
	if f.Excludes == nil {
		f.Excludes = []string{}
	}
	if f.Includes == nil {
		f.Includes = []string{}
	}
	c.unknown = f.Unknown
	return c, &f.Settings, nil
}

// save writes set to copy.json atomically, with the members this version
// doesn't know as they were loaded.
func (c *copyStore) save(set copyapp.Settings) error {
	return c.storage.write(copyName, copyFile{Settings: set, Unknown: c.unknown})
}
