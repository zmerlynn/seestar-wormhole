package ui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
)

const maxJSON = 64 << 10

var errStorage = errors.New("UI storage unavailable: use a private state directory")

type storage struct {
	root      string
	directory *os.File
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
	return &storage{root, d}, nil
}
func (s *storage) read(name string, v any) error {
	path := filepath.Join(s.root, name)
	if _, err := os.Lstat(path); err != nil {
		return err
	}
	f, err := privatefile.Open(path, os.O_RDONLY)
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
	path := filepath.Join(s.root, name)
	if _, err := os.Lstat(path); err == nil {
		f, err := privatefile.Open(path, os.O_RDONLY)
		if err != nil {
			return errStorage
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return errStorage
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) > maxJSON {
		return errStorage
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return errStorage
	}
	tmp := filepath.Join(s.root, ".ui-"+hex.EncodeToString(nonce[:]))
	f, err := privatefile.Open(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		return errStorage
	}
	defer os.Remove(tmp)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errStorage
	}
	if err = privatefile.Replace(tmp, path); err != nil {
		return errStorage
	}
	if err = privatefile.SyncDirectory(s.directory); err != nil {
		return errStorage
	}
	return nil
}
