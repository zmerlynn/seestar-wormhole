package ui

import (
	"encoding/json/jsontext"
	"errors"
	"os"
	"path/filepath"
	"strconv"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
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
// has another version or none is an error, and the app opens with Copy,
// Browse and Free up space off. A files_at_once out of range is clamped
// into it, and returned as a fallback.
func loadCopySettings(st *storage) (*copyStore, *copyapp.Settings, []diagnostic.Fallback, error) {
	c := &copyStore{storage: st}
	// Absent members take their defaults, except the version, which the
	// file must name; unknown ones are kept.
	f := copyFile{Settings: copyapp.DefaultSettings()}
	f.Version = 0
	if err := st.read(copyName, &f); os.IsNotExist(err) {
		return c, nil, nil, nil
	} else if err != nil {
		return nil, nil, nil, err
	}
	// A relative history folder would resolve against wherever the app
	// was started, so it is refused, not guessed.
	if f.Version != copyapp.SettingsVersion || f.HistoryDir != "" && !filepath.IsAbs(f.HistoryDir) {
		return nil, nil, nil, errStorage
	}
	var fallbacks []diagnostic.Fallback
	if clamped := min(max(f.FilesAtOnce, 1), copyapp.MaxFilesAtOnce); clamped != f.FilesAtOnce {
		f.FilesAtOnce = clamped
		fallbacks = append(fallbacks, diagnostic.Fallback{File: copyName, Setting: "files_at_once", Fallback: strconv.Itoa(clamped)})
	}
	if f.Excludes == nil {
		f.Excludes = []string{}
	}
	if f.Includes == nil {
		f.Includes = []string{}
	}
	if f.FreeExcludes == nil {
		f.FreeExcludes = []string{}
	}
	if f.FreeIncludes == nil {
		f.FreeIncludes = []string{}
	}
	c.unknown = f.Unknown
	return c, &f.Settings, fallbacks, nil
}

// ErrNoCopySettings is LoadCopySettings's error when root holds no
// copy.json, or there is no root.
var ErrNoCopySettings = errors.New("no saved copy settings")

// LoadCopySettings reads root's copy.json as the app loads it, without
// writing a file or taking ui.lock. A missing file or root is
// ErrNoCopySettings; one the app would refuse is another error. The
// fallbacks are the saved values it replaced, for the caller to log.
func LoadCopySettings(root string) (copyapp.Settings, []diagnostic.Fallback, error) {
	d, err := privatefile.ReadDirectory(root)
	if os.IsNotExist(err) {
		return copyapp.Settings{}, nil, ErrNoCopySettings
	} else if err != nil {
		return copyapp.Settings{}, nil, errStorage
	}
	defer d.Close()
	_, set, fallbacks, err := loadCopySettings(&storage{root: root, directory: d})
	switch {
	case err != nil:
		return copyapp.Settings{}, nil, err
	case set == nil:
		return copyapp.Settings{}, nil, ErrNoCopySettings
	}
	return *set, fallbacks, nil
}

// save writes set to copy.json atomically, with the members this version
// doesn't know as they were loaded.
func (c *copyStore) save(set copyapp.Settings) error {
	return writeCopySettings(c.storage, copyFile{Settings: set, Unknown: c.unknown})
}

// writeCopySettings is copy.json's one writer; tests replace it.
var writeCopySettings = func(st *storage, f copyFile) error { return st.write(copyName, f) }
