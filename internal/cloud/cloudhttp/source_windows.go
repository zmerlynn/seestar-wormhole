//go:build windows

package cloudhttp

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"golang.org/x/sys/windows"
)

func persistentSource(stateDir string) (string, error) {
	if stateDir == "" {
		return randomSource()
	}
	if err := privatefile.ValidatePath(stateDir); err != nil {
		return "", stateError("state_directory", diagnostic.Class(err))
	}
	// Only the identity subdirectory is security sensitive. The operator supplies
	// trusted parents; the pinned subdirectory and file refuse all reparse points.
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return "", stateError("state_directory", diagnostic.Class(err))
	}
	d, err := privatefile.Directory(filepath.Join(stateDir, "InterProcessKV"))
	if err != nil {
		return "", stateError("state_directory", diagnostic.Class(err))
	}
	defer d.Close()
	f, err := privatefile.Open(filepath.Join(stateDir, "InterProcessKV", "device_uuid"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		return "", stateError("state_identity", diagnostic.Class(err))
	}
	defer f.Close()
	return persistentIdentity(f)
}

// persistentIdentity locks before reading or repairing an already validated handle.
func persistentIdentity(f *os.File) (string, error) {
	var lock windows.Overlapped
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		// Every cooperating process locks byte zero, including an empty file. Native
		// locks exclude reads/writes by other processes too, unlike advisory flock.
		err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &lock)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return "", stateError("state_identity", diagnostic.Class(err))
		}
		select {
		case <-deadline.C:
			return "", stateError("state_identity", "lock_timeout")
		case <-tick.C:
		}
	}
	defer windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &lock)
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
	if _, err := f.WriteString(id); err != nil {
		return "", stateError("state_identity", diagnostic.Class(err))
	}
	if err := f.Sync(); err != nil {
		return "", stateError("state_identity", diagnostic.Class(err))
	}
	// Windows directory handles cannot be synced like Unix directories. The
	// WRITE_THROUGH file write flushes metadata (including NTFS changes), and
	// Sync adds FlushFileBuffers. Both must succeed; power-loss guarantees still
	// depend on filesystem/device support. Unsupported ACLs fail closed.
	return id, nil
}
