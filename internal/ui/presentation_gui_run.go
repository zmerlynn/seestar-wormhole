//go:build gui && !guitest

package ui

import (
	"context"
	"io"
)

func runPlatformUI(ctx context.Context, root string, out io.Writer, run runFunc) error {
	return runUIWithWindow(ctx, root, out, run, openBrowser, osKeychain{}, windowPresenter(root))
}
