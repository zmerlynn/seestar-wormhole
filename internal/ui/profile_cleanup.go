package ui

import (
	"fmt"
	"io"
)

// Disposable engine data must not change the result of the window lifetime.
func profileCleanupResult(out io.Writer, cleanup func() error, result error) error {
	if err := cleanup(); err != nil {
		fmt.Fprintln(out, "Warning:", err)
	}
	return result
}
