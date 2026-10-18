//go:build !windows

// Package privatefile opens files with platform-enforced private access.
package privatefile

import "os"

// Open preserves Unix creation flags and 0600 permissions.
func Open(path string, flags int) (*os.File, error) {
	return os.OpenFile(path, flags, 0600)
}
