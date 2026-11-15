package ui

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zmerlynn/seestar-wormhole/internal/localhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
)

var errStorage = errors.New("Settings storage unavailable. Use a private state directory.")

// storageError is errStorage with what failed, which its text names for
// the log, the refusal or the dialog that shows it. It unwraps to
// errStorage alone, so errors.Is finds errStorage and the cause classifies
// nothing.
type storageError struct{ cause string }

func (e *storageError) Error() string {
	return "Settings storage unavailable: " + strings.TrimSuffix(e.cause, ".") + ". Use a private state directory."
}
func (e *storageError) Unwrap() error { return errStorage }

// storageFailed is errStorage caused by err.
func storageFailed(err error) error { return &storageError{cause: err.Error()} }

// storageRefused is errStorage for a refusal that has no error behind it,
// with why in plain words.
func storageRefused(why string) error { return &storageError{cause: why} }

type storage struct {
	root            string
	directory       *os.File
	settingsUnknown Settings
	settingsRefused bool // A failed load disables settings writes for this run.
}

func openStorage(root string) (*storage, error) {
	if err := privatefile.ValidatePath(root); err != nil {
		return nil, storageFailed(err)
	}
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return nil, storageFailed(err)
	}
	d, err := privatefile.Directory(root)
	if err != nil {
		return nil, storageFailed(err)
	}
	return &storage{root: root, directory: d}, nil
}
func (s *storage) read(name string, v any) error {
	path := filepath.Join(s.root, name)
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	f, err := privatefile.OpenRead(path)
	if err != nil {
		return storageFailed(err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, localhttp.MaxJSON+1))
	if err != nil {
		return storageFailed(err)
	}
	if len(b) > localhttp.MaxJSON {
		return tooLarge(path, localhttp.MaxJSON)
	}
	if err = json.Unmarshal(b, v); err != nil {
		return storageRefused(path + ": " + err.Error())
	}
	return nil
}
func (s *storage) write(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return storageRefused(name + ": " + err.Error())
	} else if len(b) > localhttp.MaxJSON {
		return tooLarge(name, localhttp.MaxJSON)
	}
	return s.writeBytes(name, b)
}

// writeSettings writes a settings file, config.json or copy.json, as
// write does but indented two spaces a level and ending in a newline, so
// that a person can read and edit it.
func (s *storage) writeSettings(name string, v any) error {
	b, err := json.Marshal(v, jsontext.WithIndent("  "))
	if b = append(b, '\n'); err != nil {
		return storageRefused(name + ": " + err.Error())
	} else if len(b) > localhttp.MaxJSON {
		return tooLarge(name, localhttp.MaxJSON)
	}
	return s.writeBytes(name, b)
}

func (s *storage) writeBytes(name string, b []byte) error {
	if name == "config.json" && s.settingsRefused {
		return &ActionError{Code: "settings"}
	}
	path := filepath.Join(s.root, name)
	if _, err := os.Lstat(path); err == nil {
		f, err := privatefile.OpenRead(path)
		if err != nil {
			return storageFailed(err)
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return storageFailed(err)
	}
	if err := privatefile.WriteFile(s.directory, name, b, privatefile.Private); err != nil {
		return storageFailed(err)
	}
	return nil
}

// tooLarge refuses a file over limit bytes, the size the app reads or
// keeps.
func tooLarge(name string, limit int) error {
	return storageRefused(name + " is over " + strconv.Itoa(limit>>10) + " KiB")
}
