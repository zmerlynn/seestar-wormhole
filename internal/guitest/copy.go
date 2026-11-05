package guitest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

// CopyScenarios run the Copy and Free up space tabs against the fixture's
// fake telescope. copy-quit needs a Quit event, so only macOS runs it.
const CopyScenarios = "copy-close,copy-quit,copy-inuse"

// CopyFolder is a copy scenario's image folder: beside the state dir, so
// two apps with sibling state dirs share it.
func CopyFolder(root string) string { return filepath.Join(filepath.Dir(root), "images") }

// copyState is the copier's state folder, which seestar-copy-cli shares.
func copyState() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "seestar-copy"), nil
}

// CopyRecorded reports whether folder's copy history has path.
func CopyRecorded(folder, path string) bool {
	dir, err := copyState()
	if err != nil {
		return false
	}
	records, err := state.Load(dir, folder, nil)
	if err != nil {
		return false
	}
	_, ok := records[path]
	return ok
}

// CopyStopped checks, once the apps have exited, that a held transfer left
// no temporary file in folder and that its state lock is free.
func CopyStopped(folder string) error {
	err := filepath.WalkDir(folder, func(p string, d fs.DirEntry, err error) error {
		if err == nil && plan.IsTempName(d.Name()) {
			return fmt.Errorf("a temporary file is left: %s", p)
		}
		return err
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	dir, err := copyState()
	if err != nil {
		return err
	}
	st, err := state.Open(dir, folder, nil)
	if err != nil {
		return fmt.Errorf("the state lock isn't free: %w", err)
	}
	return st.Close()
}
