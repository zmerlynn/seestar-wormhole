package state

import (
	"errors"
	"os"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/filelock"
)

// ErrLocked reports that another open State holds the destination's lock.
var ErrLocked = errors.New("another sync is using this destination")

// lockFile opens or creates the lock file at path and takes its exclusive
// [filelock] lock. Without wait, a held lock fails with ErrLocked; with it,
// lockFile blocks until the lock is free. The lock belongs to the open file,
// so a second open in the same process conflicts too. It lasts until the
// returned file is closed or the process exits.
func lockFile(path string, wait bool) (*os.File, error) {
	readonly.Check("state.lockFile")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if wait {
		err = filelock.Lock(f, 0)
	} else if locked, lerr := filelock.TryLock(f); lerr != nil {
		err = lerr
	} else if !locked {
		err = ErrLocked
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
