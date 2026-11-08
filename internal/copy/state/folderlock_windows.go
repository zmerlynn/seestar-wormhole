package state

import (
	"errors"

	"golang.org/x/sys/windows"
)

// readOnlyFS reports whether err says the file system, or the medium, is
// read-only.
func readOnlyFS(err error) bool { return errors.Is(err, windows.ERROR_WRITE_PROTECT) }
