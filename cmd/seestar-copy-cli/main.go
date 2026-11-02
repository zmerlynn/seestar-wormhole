// Command seestar-copy-cli copies the files from a Seestar's SMB share to a
// local folder.
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
)

const programName = "seestar-copy-cli"

// Release builds set these with -ldflags "-X main.version=... -X main.revision=...".
var (
	version  = "development"
	revision = ""
)

// Exit codes, as documented in the command help.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run executes one invocation and returns its exit code. Help, version and
// licenses go to stdout; logs and errors go to stderr.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	logger := newLogger(stderr)
	output := &checkedWriter{Writer: stdout}
	root := newCommand(output, logger)
	root.SetArgs(append([]string{}, args...))
	executed, err := root.ExecuteContextC(ctx)
	if err == nil {
		err = output.err
	}
	if err == nil {
		return exitOK
	}
	if usage, ok := errors.AsType[*usageError](err); ok {
		logger.Error("invalid arguments", "option", usage.Option, "rule", usage.Rule,
			"help", executed.CommandPath()+" --help")
		return exitUsage
	}
	logger.Error(executed.Name()+" failed", "error", err)
	return exitFailure
}

// checkedWriter keeps the first write error, since Cobra's help callbacks
// cannot return one.
type checkedWriter struct {
	io.Writer
	err error
}

func (w *checkedWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if w.err == nil {
		w.err = err
	}
	return n, err
}

// newLogger writes slog text lines with UTC millisecond timestamps.
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

func versionString() string {
	info, _ := debug.ReadBuildInfo()
	return formatVersion(version, revision, info, runtime.GOOS, runtime.GOARCH)
}

// formatVersion prints "<program> <version> (<revision>, <os>/<arch>)". A
// revision set at link time wins; otherwise the build's VCS stamp is used,
// shortened to 12 characters as release builds do.
func formatVersion(version, revision string, info *debug.BuildInfo, goos, goarch string) string {
	if revision == "" && info != nil {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				revision = setting.Value[:min(len(setting.Value), 12)]
			}
		}
	}
	if revision == "" {
		revision = "development"
	}
	return programName + " " + version + " (" + revision + ", " + goos + "/" + goarch + ")"
}
