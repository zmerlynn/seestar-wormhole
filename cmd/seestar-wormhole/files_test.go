package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// fileArgs returns valid arguments for each file command. Nothing here is
// run against a device: the tests replace the command's run.
func fileArgs(t *testing.T) map[string][]string {
	dest := t.TempDir()
	return map[string][]string{
		"sync":   {"sync", "--endpoint", "127.0.0.1:1", "--dest", dest, "--history-dir", t.TempDir()},
		"find":   {"find", "--endpoint", "127.0.0.1:1"},
		"browse": {"browse", "--endpoint", "127.0.0.1:1"},
		"delete": {"delete", "--endpoint", "127.0.0.1:1", "--dest", dest},
	}
}

// Each command has its own logging defaults, all in one tree: proxy and
// discovery log JSON, the file commands text, and each follows its own flag.
func TestFileLoggingDefaults(t *testing.T) {
	files := fileArgs(t)
	for _, tc := range []struct {
		args   []string
		format string
	}{
		{[]string{"proxy"}, "json"},
		{discoveryArgs(), "json"},
		{files["sync"], "text"},
		{files["find"], "text"},
		{files["browse"], "text"},
		{files["delete"], "text"},
		{append(files["sync"], "--log-format", "json"), "json"},
		{[]string{"proxy", "--log-format", "text"}, "text"},
		{append(discoveryArgs(), "--log-format=text"), "text"},
	} {
		o, err := parse(tc.args, io.Discard)
		if err != nil || o.format != tc.format || o.level != "info" {
			t.Errorf("%q: format %q level %q err %v; want %s and info", tc.args, o.format, o.level, err, tc.format)
		}
	}
	o, err := parse(append(files["find"], "--log-level", "debug"), io.Discard)
	if err != nil || o.command != commandFiles || o.level != "debug" {
		t.Errorf("find --log-level debug: %+v %v", o, err)
	}
}

// A file command's usage record is text whatever --log-format says, since
// that flag may be the one at fault, and a bad logging flag is a usage error.
func TestFileUsageRecordFormat(t *testing.T) {
	files := fileArgs(t)
	for _, tc := range []struct {
		args         []string
		option, rule string
	}{
		{append(files["sync"], "--log-format", "json", "--workers", "0"), "--workers", ""},
		{append(files["find"], "--log-format=PRIVATE_VALUE"), "--log-format", "choose json or text"},
		{append(files["delete"], "--log-level", "PRIVATE_VALUE"), "--log-level", "choose debug, info, warn or error"},
		{append(files["find"], "--older-than=PRIVATE_VALUE"), "--older-than", "use a duration such as 36h, or whole days such as 7d"},
		{append(files["sync"], "PRIVATE_VALUE"), "arguments", "positional arguments are not allowed"},
	} {
		code, record, logs := usageExit(t, tc.args...)
		if code != 2 || record.Msg != tc.args[0]+" failed" || record.Option != tc.option || tc.rule != "" && record.Rule != tc.rule ||
			record.Help != programName+" "+tc.args[0]+" --help" || strings.Contains(logs, "cleanup_reason") || strings.Contains(logs, "PRIVATE_VALUE") {
			t.Errorf("%q: exit %d record %+v logs %s", tc.args, code, record, logs)
		}
	}
}

// The command shell, not internal/copy/cli, turns the read-only guard on,
// and before the command runs.
func TestFileReadOnlyGuard(t *testing.T) {
	defer func(enable func()) { enableReadOnly = enable }(enableReadOnly)
	files := fileArgs(t)
	for _, tc := range []struct {
		args     []string
		readOnly bool
	}{
		{files["find"], true},
		{files["browse"], true},
		{append(files["sync"], "--dry-run"), true},
		{files["delete"], true}, // a preview
		{files["sync"], false},
		{append(files["delete"], "--force"), false},
	} {
		var calls []string
		enableReadOnly = func() { calls = append(calls, "enable") }
		o, err := parse(tc.args, io.Discard)
		if err != nil || o.file == nil {
			t.Fatalf("%q: %v", tc.args, err)
		}
		o.file.Run = func(context.Context, io.Writer, *slog.Logger) error { calls = append(calls, "run"); return nil }
		if err := runParsed(context.Background(), o, io.Discard, io.Discard); err != nil {
			t.Fatalf("%q: %v", tc.args, err)
		}
		want := "run"
		if tc.readOnly {
			want = "enable run"
		}
		if got := strings.Join(calls, " "); got != want {
			t.Errorf("%q: calls %q, want %q", tc.args, got, want)
		}
	}
}

// A file command's failure is one record in its own format, with its name.
func TestFileFailureRecord(t *testing.T) {
	files := fileArgs(t)
	for _, format := range []string{"text", "json"} {
		var out, logs bytes.Buffer
		code := mainExitCodeWithRunner(append(files["find"], "--log-format", format), &out, &logs, noSignals,
			func() (io.Writer, func()) { return &logs, func() {} }, false,
			fakeFileRun(func(_ io.Writer, log *slog.Logger) error {
				log.Info("listing")
				return errors.New("share unavailable")
			}))
		lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
		record := decodeRecord(t, format, lines[len(lines)-1])
		if code != 1 || out.Len() != 0 || len(lines) != 2 || record["msg"] != "find failed" || record["error"] != "share unavailable" {
			t.Errorf("%s: exit %d stdout %q logs %s", format, code, &out, &logs)
		}
	}
}

func noSignals(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
	return ctx, func() {}
}

// fakeFileRun returns a runner that replaces the parsed file command's work
// with run, then runs it as the program does.
func fakeFileRun(run func(stdout io.Writer, log *slog.Logger) error) func(context.Context, options, io.Writer, io.Writer) error {
	return func(ctx context.Context, o options, out, logs io.Writer) error {
		o.file.ReadOnly = false
		o.file.Run = func(_ context.Context, stdout io.Writer, log *slog.Logger) error { return run(stdout, log) }
		return runParsed(ctx, o, out, logs)
	}
}

// decodeRecord decodes one log line in format, failing if it is in the
// other format.
func decodeRecord(t *testing.T, format, line string) map[string]any {
	t.Helper()
	var record map[string]any
	jsonErr := json.Unmarshal([]byte(line), &record)
	if format == "json" {
		if jsonErr != nil {
			t.Fatalf("want a JSON record: %q: %v", line, jsonErr)
		}
		return record
	}
	if jsonErr == nil {
		t.Fatalf("want a text record, got JSON: %q", line)
	}
	record, err := textRecord(line)
	if err != nil {
		t.Fatalf("want a text record: %q: %v", line, err)
	}
	return record
}

// lossSink fails writes while failing is set, and records the rest.
type lossSink struct {
	bytes.Buffer
	failing bool
}

func (s *lossSink) Write(p []byte) (int, error) {
	if s.failing {
		return 0, io.ErrClosedPipe
	}
	return s.Buffer.Write(p)
}

// A file command that loses diagnostics exits 1, keeps them off stdout,
// whose contents are the command's output, and accounts for them on stderr
// in its own log format.
func TestFileDiagnosticsLoss(t *testing.T) {
	const listing = "- 1 2026-10-31T01:00:00Z a.fit\n"
	t.Run("permanent", func(t *testing.T) {
		sink := &lossSink{failing: true}
		var out bytes.Buffer
		code := mainExitCodeWithRunner([]string{"find", "--endpoint", "127.0.0.1:1", "--format", "jsonl"}, &out, io.Discard, noSignals,
			func() (io.Writer, func()) { return &logLossWriter{output: sink}, func() {} }, false,
			fakeFileRun(func(stdout io.Writer, log *slog.Logger) error {
				log.Info("listing")
				_, err := io.WriteString(stdout, listing)
				log.Info("listing done")
				return err
			}))
		if code != 1 || out.String() != listing || sink.Len() != 0 {
			t.Errorf("exit %d stdout %q stderr %q; want 1 and only the listing", code, &out, sink)
		}
	})
	for _, format := range []string{"text", "json"} {
		t.Run("recovered/"+format, func(t *testing.T) {
			sink := &lossSink{failing: true}
			var out bytes.Buffer
			code := mainExitCodeWithRunner([]string{"find", "--endpoint", "127.0.0.1:1", "--log-format", format}, &out, io.Discard, noSignals,
				func() (io.Writer, func()) { return &logLossWriter{output: sink}, func() {} }, false,
				fakeFileRun(func(stdout io.Writer, log *slog.Logger) error {
					log.Info("lost")
					sink.failing = false
					log.Info("recovered")
					_, err := io.WriteString(stdout, listing)
					return err
				}))
			var notices int
			for line := range strings.SplitSeq(strings.TrimSpace(sink.String()), "\n") {
				record := decodeRecord(t, format, line)
				if record["msg"] == "diagnostics_lost" {
					notices++
					if record["rule"] != "log_sink_loss" || record["level"] != "WARN" {
						t.Errorf("notice %q", line)
					}
				}
			}
			// The notice when stderr recovers, and the final summary.
			if code != 1 || out.String() != listing || notices != 2 {
				t.Errorf("exit %d stdout %q stderr %s; want 1, the listing and two notices", code, &out, sink)
			}
		})
	}
	t.Run("shutdown", func(t *testing.T) {
		var sink bytes.Buffer
		w := &logLossWriter{output: &sink}
		var out bytes.Buffer
		code := mainExitCodeWithRunner([]string{"find", "--endpoint", "127.0.0.1:1"}, &out, io.Discard, noSignals,
			func() (io.Writer, func()) { return w, func() { w.lose(0) } }, false,
			fakeFileRun(func(stdout io.Writer, log *slog.Logger) error {
				log.Info("listing done")
				_, err := io.WriteString(stdout, listing)
				return err
			}))
		if code != 1 || out.String() != listing {
			t.Errorf("exit %d stdout %q; want 1 and only the listing", code, &out)
		}
	})
	t.Run("failure wins", func(t *testing.T) {
		sink := &lossSink{failing: true}
		code := mainExitCodeWithRunner([]string{"find", "--endpoint", "127.0.0.1:1", "--format", "PRIVATE_VALUE"}, io.Discard, io.Discard, noSignals,
			func() (io.Writer, func()) { return &logLossWriter{output: sink}, func() {} }, false, runParsed)
		if code != 2 {
			t.Errorf("usage error with lost diagnostics: exit %d, want 2", code)
		}
	})
}

// proxy keeps its stdout fallback for lost diagnostics.
func TestProxyDiagnosticsFallback(t *testing.T) {
	sink := &lossSink{failing: true}
	var out bytes.Buffer
	code := mainExitCodeWithRunner([]string{"proxy"}, &out, io.Discard, noSignals,
		func() (io.Writer, func()) { return &logLossWriter{output: sink}, func() {} }, false,
		func(_ context.Context, _ options, _, logs io.Writer) error {
			newLogger("json", logs).Info("lost")
			return nil
		})
	if code != 1 || !strings.Contains(out.String(), `"rule":"log_sink_loss"`) {
		t.Errorf("exit %d stdout %q", code, &out)
	}
}
