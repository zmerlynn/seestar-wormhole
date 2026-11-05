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

func versionString() string {
	return buildinfo.String(programName, version, revision)
}
