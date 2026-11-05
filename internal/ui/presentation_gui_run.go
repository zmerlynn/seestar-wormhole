//go:build gui && !guitest

package ui

import (
	"context"
	"io"
)

func runPlatformUI(ctx context.Context, root string, out, logs io.Writer, run runFunc) error {
	window, native := windowPresenter(root, out)
	return runUIPresented(ctx, root, out, logs, run, openBrowser, osKeychain{}, window, native)
}
