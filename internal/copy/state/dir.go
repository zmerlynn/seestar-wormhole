package state

import (
	"errors"
	"os"
	"path/filepath"
)

// DefaultDir returns the copy history folder used when none is given: the
// per-user config folder's seestar-copy. The file commands and the app's
// copier both use it, so they share one history and one lock per
// destination. The name predates the merge into seestar-wormhole and is
// kept so that existing history is found without a migration.
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", errors.New("no per-user config folder; give --history-dir")
	}
	return filepath.Join(base, "seestar-copy"), nil
}

// HistoryDir returns explicit if it is not empty, or [DefaultDir].
func HistoryDir(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	return DefaultDir()
}
