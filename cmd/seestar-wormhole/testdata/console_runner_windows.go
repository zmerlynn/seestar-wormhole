//go:build gui

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Compiled only through the regression test's overlay. The real main and
// bootstrap run unchanged; only the command's runner is replaced.
func consoleRegressionRunner(ctx context.Context, _ options, _, _ io.Writer) error {
	dir := os.Getenv("WORMHOLE_CONSOLE_TEST_DIR")
	if err := os.WriteFile(filepath.Join(dir, "ready"), []byte("ready\n"), 0600); err != nil {
		return err
	}
	<-ctx.Done()
	// Model release work that must finish after cancellation, before exit.
	time.Sleep(150 * time.Millisecond)
	return os.WriteFile(filepath.Join(dir, "released"), []byte("cleanup complete\n"), 0600)
}
