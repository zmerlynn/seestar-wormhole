//go:build !windows

package state

import (
	"errors"
	"syscall"
)

// readOnlyFS reports whether err says the file system is read-only.
func readOnlyFS(err error) bool { return errors.Is(err, syscall.EROFS) }
