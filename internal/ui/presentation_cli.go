//go:build !gui

package ui

import (
	"context"
	"errors"
	"io"
)

func runPlatformUI(ctx context.Context, root string, out, logs io.Writer, run runFunc) error {
	return errors.New("UI requires the seestar-wormhole GUI build")
}
