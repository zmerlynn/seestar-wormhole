//go:build unix

package cloudhttp

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/filelock"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// persistentSource returns a fresh in-memory UUID for an empty directory.
// With an explicit directory it guards InterProcessKV/device_uuid.
// A Linux advisory lock makes first creation/empty-file repair atomic between
// cooperating processes. Lock acquisition is bounded; no secret/path escapes in
// an OS diagnostic. The file contains only the installation UUID, never login
// credentials. Existing nonempty identifiers are retained verbatim.
func persistentSource(stateDir string) (string, error) {
	if stateDir == "" {
		return randomSource()
	}
	dir := filepath.Join(stateDir, "InterProcessKV")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", stateError(stateDir, "state_directory", diagnostic.Class(err), err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", stateError(stateDir, "state_directory", diagnostic.Class(err), err)
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", stateError(stateDir, "state_directory", "unsafe_permissions_or_type", nil)
	}
	// O_NOFOLLOW rejects a substituted credential-file symlink. Linux is the
	// release target. File mode is checked after opening the exact inode we lock.
	f, err := os.OpenFile(filepath.Join(dir, "device_uuid"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return "", stateError(stateDir, "state_identity", diagnostic.Class(err), err)
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return "", stateError(stateDir, "state_identity", diagnostic.Class(err), err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", stateError(stateDir, "state_identity", "unsafe_permissions_or_type", nil)
	}
	switch err := filelock.Lock(f, time.Second); {
	case errors.Is(err, filelock.ErrTimeout):
		return "", stateError(stateDir, "state_identity", "lock_timeout", nil)
	case err != nil:
		return "", stateError(stateDir, "state_identity", diagnostic.Class(err), err)
	}
	defer filelock.Unlock(f)
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if len(b) > 4096 {
		return "", stateError(stateDir, "state_identity", "invalid_content", nil)
	}
	if err != nil {
		return "", stateError(stateDir, "state_identity", diagnostic.Class(err), err)
	}
	if len(b) > 0 {
		if !validInput(string(b)) {
			return "", stateError(stateDir, "state_identity", "invalid_content", nil)
		}
		return string(b), nil
	}
	id, err := randomSource()
	if err != nil {
		return "", err
	}
	if _, err = f.WriteString(id); err != nil {
		return "", stateError(stateDir, "state_identity", diagnostic.Class(err), err)
	}
	if err = f.Sync(); err != nil {
		return "", stateError(stateDir, "state_identity", diagnostic.Class(err), err)
	}
	// Sync the directory entry as well as its content for restart durability.
	d, err := os.Open(dir)
	if err != nil {
		return "", stateError(stateDir, "state_identity", diagnostic.Class(err), err)
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return "", stateError(stateDir, "state_identity", diagnostic.Class(err), err)
	}
	return id, nil
}
