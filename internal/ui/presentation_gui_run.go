//go:build gui && !guitest

package ui

import (
	"context"
	"io"
)

func runPlatformUI(ctx context.Context, root string, out io.Writer, run runFunc) error {
	return runUIPresented(ctx, root, out, run, openBrowser, osKeychain{}, windowPresenter(root, out), true)
}
