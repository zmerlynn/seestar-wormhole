//go:build unix

package cloudhttp

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
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
		return "", stateError("state_directory", diagnostic.Class(err))
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", stateError("state_directory", diagnostic.Class(err))
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", stateError("state_directory", "unsafe_permissions_or_type")
	}
	// O_NOFOLLOW rejects a substituted credential-file symlink. Linux is the
	// release target. File mode is checked after opening the exact inode we lock.
	f, err := os.OpenFile(filepath.Join(dir, "device_uuid"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return "", stateError("state_identity", diagnostic.Class(err))
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return "", stateError("state_identity", diagnostic.Class(err))
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return "", stateError("state_identity", "unsafe_permissions_or_type")
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return "", stateError("state_identity", diagnostic.Class(err))
		}
		select {
		case <-deadline.C:
			return "", stateError("state_identity", "lock_timeout")
		case <-tick.C:
		}
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if len(b) > 4096 {
		return "", stateError("state_identity", "invalid_content")
	}
	if err != nil {
		return "", stateError("state_identity", diagnostic.Class(err))
	}
	if len(b) > 0 {
		if !validInput(string(b)) {
			return "", stateError("state_identity", "invalid_content")
		}
		return string(b), nil
	}
	id, err := randomSource()
	if err != nil {
		return "", err
	}
	if _, err = f.WriteString(id); err != nil {
		return "", stateError("state_identity", diagnostic.Class(err))
	}
	if err = f.Sync(); err != nil {
		return "", stateError("state_identity", diagnostic.Class(err))
	}
	// Sync the directory entry as well as its content for restart durability.
	d, err := os.Open(dir)
	if err != nil {
		return "", stateError("state_identity", diagnostic.Class(err))
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return "", stateError("state_identity", diagnostic.Class(err))
	}
	return id, nil
}
