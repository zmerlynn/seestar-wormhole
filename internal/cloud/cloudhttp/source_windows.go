//go:build windows

package cloudhttp

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/filelock"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
)

func persistentSource(stateDir string) (string, error) {
	if stateDir == "" {
		return randomSource()
	}
	if err := privatefile.ValidatePath(stateDir); err != nil {
		return "", stateError(stateDir, "state_directory", diagnostic.Class(err), err)
	}
	// Only the identity subdirectory is security sensitive. The operator supplies
	// trusted parents; the pinned subdirectory and file refuse all reparse points.
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return "", stateError(stateDir, "state_directory", diagnostic.Class(err), err)
	}
	d, err := privatefile.Directory(filepath.Join(stateDir, "InterProcessKV"))
	if err != nil {
		return "", stateError(stateDir, "state_directory", diagnostic.Class(err), err)
	}
	defer d.Close()
	f, err := privatefile.Open(filepath.Join(stateDir, "InterProcessKV", "device_uuid"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		return "", stateError(stateDir, "state_identity", diagnostic.Class(err), err)
	}
	defer f.Close()
	return persistentIdentity(stateDir, f)
}

// persistentIdentity locks before reading or repairing an already validated
// handle. Its errors name stateDir.
func persistentIdentity(stateDir string, f *os.File) (string, error) {
	// Every cooperating process locks byte zero, including an empty file. Native
	// locks exclude reads/writes by other processes too, unlike advisory flock.
	switch err := filelock.Lock(f, time.Second); {
	case errors.Is(err, filelock.ErrTimeout):
		return "", stateError(stateDir, "state_identity", "lock_timeout", nil)
	case err != nil:
		return "", stateError(stateDir, "state_identity", diagnostic.Class(err), err)
	}
	defer filelock.Unlock(f)
	return readOrRepairIdentity(stateDir, f)
}

// readOrRepairIdentity requires the caller to hold the native identity lock.
func readOrRepairIdentity(stateDir string, f *os.File) (string, error) {
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
	if _, err := f.WriteString(id); err != nil {
		return "", stateError(stateDir, "state_identity", diagnostic.Class(err), err)
	}
	if err := f.Sync(); err != nil {
		return "", stateError(stateDir, "state_identity", diagnostic.Class(err), err)
	}
	// Windows directory handles cannot be synced like Unix directories. The
	// WRITE_THROUGH file write flushes metadata (including NTFS changes), and
	// Sync adds FlushFileBuffers. Both must succeed; power-loss guarantees still
	// depend on filesystem/device support. Unsupported ACLs fail closed.
	return id, nil
}
