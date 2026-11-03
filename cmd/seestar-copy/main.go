// Command seestar-copy is the desktop app: it copies a Seestar's images to
// a folder and browses the telescope's files, from one window per settings
// file.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/zmerlynn/seestar-copy/internal/app"
)

// Builds set this with -ldflags "-X main.version=...".
var version = "development"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run starts the app and returns the exit code: 0 once it is closed, 1 if
// it could not start or failed, 2 for bad arguments. Errors and logs go to
// stderr.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := pflag.NewFlagSet("seestar-copy", pflag.ContinueOnError)
	flags.SetOutput(stderr)
	settings := flags.String("settings", "", "the settings `file` (default: app.json in the per-user config folder's seestar-copy folder)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "seestar-copy takes no arguments, only --settings FILE; got %q\n", flags.Arg(0))
		return 2
	}
	if flags.Changed("settings") && *settings == "" {
		fmt.Fprintln(stderr, "--settings needs a file")
		return 2
	}
	// The app uses the CLI's default state folder, so they share one
	// history per folder.
	base, err := os.UserConfigDir()
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't find the per-user config folder: %v\n", err)
		return 1
	}
	stateDir := filepath.Join(base, "seestar-copy")
	path := *settings
	if path == "" {
		path = filepath.Join(stateDir, "app.json")
	}
	err = app.Run(ctx, app.Options{SettingsPath: path, StateDir: stateDir, Version: version, Log: newLogger(stderr)}, present(stdout))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// newLogger writes slog text lines with UTC millisecond timestamps, as the
// CLI does.
func newLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 {
				return slog.String(slog.TimeKey, a.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z"))
			}
			return a
		},
	}))
}
