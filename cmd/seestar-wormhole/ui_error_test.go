package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/spf13/cobra"
)

func TestUIStartupErrorReporter(t *testing.T) {
	saved := reportUIError
	defer func() { reportUIError = saved }()
	for _, args := range [][]string{{"ui"}, {"ui", "--unknown"}, {"proxy"}} {
		calls := 0
		reportUIError = func(error) { calls++ }
		mainExitCodeWithRunner(args, io.Discard, io.Discard,
			func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
			func() (io.Writer, func()) { return io.Discard, func() {} }, true,
			func(context.Context, options, io.Writer, io.Writer) error { return errors.New("startup failed") })
		want := 1
		if !guiEnabled || args[0] == "proxy" {
			want = 0
		}
		if calls != want {
			t.Fatalf("%q: startup notifications %d, want %d", args, calls, want)
		}
	}
}

// rawStderr fails the test if anything bypasses the log sink.
type rawStderr struct{ t *testing.T }

func (w rawStderr) Write(p []byte) (int, error) {
	w.t.Errorf("written to raw stderr: %q", p)
	return len(p), nil
}

// ui --help that cannot be written is an output failure, not a UI startup
// failure: one "ui failed" record through the log sink, exit 1, no startup
// dialog and nothing run. The GUI's ui command is registered in this build
// so the path runs in every test run.
func TestUIHelpOutputFailureShowsNoDialog(t *testing.T) {
	savedReport, savedRegister := reportUIError, registerUICommand
	defer func() { reportUIError, registerUICommand = savedReport, savedRegister }()
	registerUICommand = func(root *cobra.Command, o *options) { root.AddCommand(newUICommand(o)) }
	for _, windows := range []bool{false, true} {
		calls, executed := 0, false
		reportUIError = func(error) { calls++ }
		var logs bytes.Buffer
		code := mainExitCodeWithRunner([]string{"ui", "--help"}, failedOutput{}, rawStderr{t},
			func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
			func() (io.Writer, func()) { return &logLossWriter{output: &logs}, func() {} }, windows,
			func(context.Context, options, io.Writer, io.Writer) error { executed = true; return nil })
		var record struct{ Level, Msg, Error string }
		err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true))
		if code != 1 || calls != 0 || executed || err != nil || strings.Count(logs.String(), "\n") != 1 ||
			record.Level != "ERROR" || record.Msg != "ui failed" || record.Error != syscall.EIO.Error() {
			t.Errorf("windows=%t: exit %d, dialogs %d, executed %t, logs %q (%v); want exit 1, no dialog, nothing run and one ui failed record",
				windows, code, calls, executed, &logs, err)
		}
	}
}
