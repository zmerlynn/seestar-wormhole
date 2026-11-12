package guitest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

// CopyScenarios run the Copy and Free up space tabs against the fixture's
// fake telescope. copy-quit needs a Quit event, so only macOS runs it.
const CopyScenarios = "copy-close,copy-quit,copy-inuse,copy-save"

// CopyFolder is a copy scenario's image folder: beside the state dir, so
// two apps with sibling state dirs share it.
func CopyFolder(root string) string { return filepath.Join(filepath.Dir(root), "images") }

// UserConfig points the per-user config folder, where the copier keeps
// its state, at dir for this process and the apps it starts, so a copy
// scenario never touches the user's own. Sibling apps it starts share it.
// restore puts the old value back.
func UserConfig(dir string) (restore func(), err error) {
	name := map[string]string{"darwin": "HOME", "windows": "AppData"}[runtime.GOOS]
	if name == "" {
		name = "XDG_CONFIG_HOME"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	old, had := os.LookupEnv(name)
	if err := os.Setenv(name, dir); err != nil {
		return nil, err
	}
	return func() {
		if had {
			os.Setenv(name, old)
		} else {
			os.Unsetenv(name)
		}
	}, nil
}

// copyState is the copier's state folder, which the CLI's file commands share.
func copyState() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "seestar-wormhole", "copy-history"), nil
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
