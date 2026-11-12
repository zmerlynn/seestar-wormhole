package state

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
)

// errNoConfig is the default folders' error when there is no per-user
// config folder.
var errNoConfig = errors.New("no per-user config folder; give --history-dir")

// DefaultDir returns the copy history folder used when none is given:
// copy-history in the app's settings folder, the per-user config folder's
// seestar-wormhole. The file commands and the app's copier both use it, so
// they share one history and one lock per destination. Earlier releases
// used the per-user config folder's seestar-copy; [HistoryDir] moves that
// folder here once, and [ReadHistoryDir] reads it in place until then.
func DefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", errNoConfig
	}
	return filepath.Join(base, "seestar-wormhole", "copy-history"), nil
}

// oldDefaultDir is the default copy history folder of earlier releases.
func oldDefaultDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", errNoConfig
	}
	return filepath.Join(base, "seestar-copy"), nil
}

// HistoryDir returns the history folder of a run that writes: explicit if
// it is not empty, or [DefaultDir] after [MoveOldHistory].
func HistoryDir(explicit string, log *slog.Logger) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	if err := MoveOldHistory(dir, log); err != nil {
		return "", err
	}
	return dir, nil
}

// ReadHistoryDir returns the history folder of a read-only run: explicit
// if it is not empty, or [HistoryToRead] of [DefaultDir].
func ReadHistoryDir(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return HistoryToRead(dir), nil
}

// HistoryToRead returns the folder a read-only run reads for dir: the old
// default folder, seestar-copy, while dir is [DefaultDir], does not exist
// and the old one does; otherwise dir. It changes nothing on disk.
func HistoryToRead(dir string) string {
	if old, ok := oldToMove(dir); ok {
		return old
	}
	return dir
}

// rename moves the old default folder; tests replace it.
var rename = os.Rename

// MoveOldHistory moves the old default folder, seestar-copy, to dir once,
// before a run that writes uses dir: only when dir is [DefaultDir], does
// not exist, and the old folder does. It first creates dir's parent, the
// app's settings folder, private as the app creates it, and logs the move
// at info level; a nil log means slog.Default(). When both folders exist it
// changes nothing, and dir is used. If another process moves the folder
// first, it finds dir in place. A rename that fails, for example while an
// older release holds the history open on Windows, is an error that names
// both folders; the old folder is never used instead.
func MoveOldHistory(dir string, log *slog.Logger) error {
	readonly.Check("state.MoveOldHistory")
	old, ok := oldToMove(dir)
	if !ok {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	err := createPrivate(filepath.Dir(dir))
	if err == nil {
		if err = rename(old, dir); err != nil {
			if _, statErr := os.Lstat(dir); statErr == nil {
				// Another process moved it first.
				return nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("couldn't move the copy history from %s to %s: %w; stop any other copy, such as an older seestar-copy-cli, and try again, or move the folder by hand", old, dir, err)
	}
	log.Info("moved the copy history", "from", old, "to", dir)
	return nil
}

// oldToMove returns the old default folder when dir is [DefaultDir], dir
// does not exist and the old folder does.
func oldToMove(dir string) (string, bool) {
	if def, err := DefaultDir(); err != nil || dir != def {
		return "", false
	}
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	old, err := oldDefaultDir()
	if err != nil {
		return "", false
	}
	if _, err := os.Lstat(old); err != nil {
		return "", false
	}
	return old, true
}

// createPrivate creates dir, with any missing parents, as the app creates
// its settings folder: dir private to the user, and the parents 0700.
func createPrivate(dir string) error {
	readonly.Check("state.createPrivate")
	if err := privatefile.ValidatePath(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	d, err := privatefile.Directory(dir)
	if err != nil {
		return err
	}
	return d.Close()
}
