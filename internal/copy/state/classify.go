package state

import (
	"errors"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// Classified is err as the diagnostic trail reads it: a history or folder
// lock held elsewhere is an open that found the destination busy. Any other
// error is returned unchanged. The app's outcomes and the command line's
// failure records both use it, so the two agree.
func Classified(err error) error {
	if errors.Is(err, ErrLocked) || errors.Is(err, ErrFolderLocked) {
		return diagnostic.Wrap("open", "busy", err)
	}
	return err
}
