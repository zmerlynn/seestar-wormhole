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
	"syscall"

	"github.com/zmerlynn/seestar-wormhole/internal/buildinfo"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/cli"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

const programName = "seestar-copy-cli"

// Release builds set these with -ldflags "-X main.version=... -X main.revision=...";
// buildinfo fills in a development build's.
var version, revision string

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
// licenses go to stdout; logs and errors go to stderr, as slog text lines
// with UTC millisecond timestamps.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	logger := diagnostic.Logger("text", stderr, slog.LevelInfo)
	output := &checkedWriter{Writer: stdout}
	root, files := newCommand(output)
	root.SetArgs(append([]string{}, args...))
	executed, err := root.ExecuteContextC(ctx)
	if err == nil {
		err = runFile(ctx, files, output, logger)
	}
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

// dial and dialRemover are set by binary_test.go, which predates the move of
// the file commands to internal/copy/cli. The commands dial through that
// package's own seams, and no in-process test here gets as far as a session.
var (
	dial        func(context.Context, string, *slog.Logger) (remote.Session, error)
	dialRemover func(context.Context, string, *slog.Logger) (remote.Remover, error)
)

// runFile runs the file command that validated, if any. A read-only one
// runs with the read-only guard on: from then on, any function that changes
// the local filesystem or the device panics.
func runFile(ctx context.Context, files []*cli.Command, stdout io.Writer, log *slog.Logger) error {
	for _, c := range files {
		if c.Run != nil {
			if c.ReadOnly {
				readonly.Enable()
			}
			return c.Run(ctx, stdout, log)
		}
	}
	return nil
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

func versionString() string {
	return buildinfo.String(programName, version, revision)
}
