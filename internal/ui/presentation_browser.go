//go:build !gui

package ui

import (
	"context"
	"io"
)

func runPlatformUI(ctx context.Context, root string, out io.Writer, run runFunc) error {
	return runUI(ctx, root, out, run, openBrowser, osKeychain{})
}
