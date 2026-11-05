//go:build gui && !guitest

package ui

import (
	"context"
	"io"
)

func runPlatformUI(ctx context.Context, root string, out, logs io.Writer, run runFunc) error {
	return runUIPresented(ctx, root, out, logs, run, openBrowser, osKeychain{}, windowPresenter(root, out), true)
}
