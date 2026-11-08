package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// programName is the program the tests mount the commands in.
const programName = "seestar-copy-cli"

// Exit codes of run.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// errNoDevice is what dial and dialRemover return in tests that do not
// replace them, so no test connects anywhere.
var errNoDevice = errors.New("no device in tests")

func TestMain(m *testing.M) {
	dial = func(context.Context, string, *slog.Logger) (remote.Session, error) { return nil, errNoDevice }
	dialRemover = func(context.Context, string, *slog.Logger) (remote.Remover, error) { return nil, errNoDevice }
	os.Exit(m.Run())
}

// run mounts the file commands in a bare root, as a program does, and runs
// one invocation: a usage error is one text line and exit 2, a failure one
// line and exit 1. The read-only guard is turned on as the program turns
// it on; the caller resets it.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	log := diagnostic.Logger("text", stderr, slog.LevelInfo)
	root := &cobra.Command{Use: programName, SilenceErrors: true, SilenceUsage: true, Args: noArguments}
	root.SetOut(stdout)
	root.SetErr(io.Discard)
	root.SetFlagErrorFunc(flagError)
	commands := Commands(programName)
	for _, c := range commands {
		root.AddCommand(c.Command)
	}
	root.SetArgs(append([]string{}, args...))
	executed, err := root.ExecuteContextC(ctx)
	for _, c := range commands {
		if err == nil && c.Run != nil {
			if c.ReadOnly {
				readonly.Enable()
			}
			err = c.Run(ctx, stdout, log)
		}
	}
	if err == nil {
		return exitOK
	}
	if usage, ok := errors.AsType[*diagnostic.OptionError](err); ok {
		log.Error("invalid arguments", "option", usage.Option, "rule", usage.Rule, "help", executed.CommandPath()+" --help")
		return exitUsage
	}
	log.Error(executed.Name()+" failed", "error", err)
	return exitFailure
}

// flagError maps the flag parser's errors as the program does, naming the
// flag.
func flagError(_ *cobra.Command, err error) error {
	if unknown, ok := errors.AsType[*pflag.NotExistError](err); ok {
		return &diagnostic.OptionError{Option: "--" + unknown.GetSpecifiedName(), Rule: "unknown flag"}
	}
	if missing, ok := errors.AsType[*pflag.ValueRequiredError](err); ok {
		return &diagnostic.OptionError{Option: "--" + missing.GetFlag().Name, Rule: "requires a value"}
	}
	if invalid, ok := errors.AsType[*pflag.InvalidValueError](err); ok {
		rule, ok := ValueRule(invalid.GetFlag().Value.Type())
		if !ok {
			rule = "use a supported value"
		}
		return &diagnostic.OptionError{Option: "--" + invalid.GetFlag().Name, Rule: rule}
	}
	return &diagnostic.OptionError{Option: "arguments", Rule: "use only supported options"}
}

// scratch returns --dest and --state-dir flags naming new empty folders, and
// --once, for an invocation that gets past the flag checks.
func scratch(t *testing.T) []string {
	return []string{"--dest", t.TempDir(), "--state-dir", t.TempDir(), "--once"}
}

// invoke runs one in-process invocation.
func invoke(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errs bytes.Buffer
	code = run(context.Background(), args, &out, &errs)
	return code, out.String(), errs.String()
}

// failingWriter accepts the first limit bytes, then fails every write.
type failingWriter struct{ limit int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit {
		n := w.limit
		w.limit = 0
		return n, errors.New("output unavailable")
	}
	w.limit -= len(p)
	return len(p), nil
}
