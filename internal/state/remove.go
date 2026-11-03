package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/zmerlynn/seestar-copy/internal/readonly"
)

// Remove deletes the state file of dest, so that sync forgets what it copied
// there: the next pass adopts the files still in dest and copies again the
// rest. It takes dest's lock for the removal, so it fails with an error
// wrapping [ErrLocked] while a sync uses dest. A missing state file is not an
// error. The lock file stays, as it always does.
func Remove(stateDir, dest string) error {
	readonly.Check("state.Remove")
	path, absDest, err := resolve(stateDir, dest)
	if err != nil {
		return err
	}
	lockPath := strings.TrimSuffix(path, ".jsonl") + ".lock"
	if _, err := os.Stat(filepath.Dir(path)); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	lock, err := lockFile(lockPath, false)
	if errors.Is(err, ErrLocked) {
		return fmt.Errorf("destination %s: %w (lock file %s)", absDest, ErrLocked, lockPath)
	}
	if err != nil {
		return fmt.Errorf("lock %s for destination %s: %w", lockPath, absDest, err)
	}
	defer lock.Close()
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
