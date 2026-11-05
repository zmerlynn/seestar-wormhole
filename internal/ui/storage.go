package ui

import (
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
)

const maxJSON = 64 << 10

var errStorage = errors.New("Settings storage unavailable. Use a private state directory.")

type storage struct {
	root            string
	directory       *os.File
	settingsUnknown Settings
	settingsRefused bool // A failed load disables settings writes for this run.
}

func openStorage(root string) (*storage, error) {
	if err := privatefile.ValidatePath(root); err != nil {
		return nil, errStorage
	}
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return nil, errStorage
	}
	d, err := privatefile.Directory(root)
	if err != nil {
		return nil, errStorage
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
		return errStorage
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxJSON+1))
	if err != nil || len(b) > maxJSON {
		return errStorage
	}
	if err = json.Unmarshal(b, v); err != nil {
		return errStorage
	}
	return nil
}
func (s *storage) write(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil || len(b) > maxJSON {
		return errStorage
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
			return errStorage
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return errStorage
	}
	if err := privatefile.WriteFile(s.directory, name, b, privatefile.Private); err != nil {
		return errStorage
	}
	return nil
}
