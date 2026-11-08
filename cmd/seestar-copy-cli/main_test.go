package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

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

func TestHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"-h"}, {"help"}} {
		code, stdout, stderr := invoke(t, args...)
		if code != exitOK || stderr != "" {
			t.Errorf("%q: code=%d stderr=%q, want 0 and no stderr", args, code, stderr)
		}
		for _, want := range []string{"Usage:\n  seestar-copy-cli [flags]\n", "  sync ", "  find ", "  licenses ", "--version"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("%q: help lacks %q:\n%s", args, want, stdout)
			}
		}
		if !strings.HasSuffix(stdout, "Third-party license information: seestar-copy-cli licenses\n") {
			t.Errorf("%q: help lacks the final license pointer:\n%s", args, stdout)
		}
	}
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

func TestHelpOutputFailure(t *testing.T) {
	_, help, _ := invoke(t, "--help")
	pointer := strings.LastIndex(help, "Third-party license information")
	for _, tc := range []struct {
		args    []string
		command string
		limit   int
	}{
		{nil, "seestar-copy-cli", 0},
		{[]string{"--help"}, "seestar-copy-cli", 0},
		{[]string{"-h"}, "seestar-copy-cli", 0},
		{[]string{"help"}, "help", 0},
		{[]string{"help", "sync"}, "help", 0},
		{[]string{"sync", "--help"}, "sync", 0},
		{[]string{"licenses", "--help"}, "licenses", 0},
		// Only the license pointer, written after Cobra's help, fails.
		{nil, "seestar-copy-cli", pointer},
		{[]string{"help"}, "help", pointer},
	} {
		var logs bytes.Buffer
		code := run(context.Background(), tc.args, &failingWriter{limit: tc.limit}, &logs)
		want := `level=ERROR msg="` + tc.command + ` failed" error="output unavailable"` + "\n"
		if code != exitFailure || !strings.HasSuffix(logs.String(), want) || strings.Count(logs.String(), "\n") != 1 {
			t.Errorf("%q, limit %d: code=%d stderr=%q, want 1 and one line ending %q", tc.args, tc.limit, code, logs.String(), want)
		}
	}
}

// recoveringWriter fails its first write, then accepts every write.
type recoveringWriter struct{ failed bool }

func (w *recoveringWriter) Write(p []byte) (int, error) {
	if !w.failed {
		w.failed = true
		return 0, errors.New("first write failed")
	}
	return len(p), nil
}

// Later successful writes must not erase the first error: the help is
// still truncated.
func TestHelpKeepsFirstOutputError(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		command string
	}{
		{nil, "seestar-copy-cli"},
		{[]string{"--help"}, "seestar-copy-cli"},
		{[]string{"help"}, "help"},
	} {
		var logs bytes.Buffer
		code := run(context.Background(), tc.args, &recoveringWriter{}, &logs)
		want := `level=ERROR msg="` + tc.command + ` failed" error="first write failed"` + "\n"
		if code != 1 || !strings.HasSuffix(logs.String(), want) || strings.Count(logs.String(), "\n") != 1 {
			t.Errorf("%q: code=%d stderr=%q, want 1 and one line ending %q", tc.args, code, logs.String(), want)
		}
	}
}

func TestHelpTopics(t *testing.T) {
	for _, topic := range []string{"sync", "licenses"} {
		code, stdout, stderr := invoke(t, "help", topic)
		_, want, _ := invoke(t, topic, "--help")
		if code != exitOK || stderr != "" || stdout != want {
			t.Errorf("help %s: code=%d stdout=%q stderr=%q, want 0 and the output of %s --help", topic, code, stdout, stderr, topic)
		}
	}
	for _, args := range [][]string{{"help", "not-a-command"}, {"help", "sync", "extra"}} {
		code, stdout, stderr := invoke(t, args...)
		want := `msg="invalid arguments" option=help rule="unknown help topic; give a command name, such as sync" help="seestar-copy-cli help --help"` + "\n"
		if code != exitUsage || stdout != "" || !strings.HasSuffix(stderr, want) || strings.Count(stderr, "\n") != 1 {
			t.Errorf("%q: code=%d stdout=%q stderr=%q, want 2, no stdout and one line ending %q", args, code, stdout, stderr, want)
		}
	}
}

func TestVersion(t *testing.T) {
	code, stdout, stderr := invoke(t, "--version")
	if code != exitOK || stderr != "" || stdout != versionString()+"\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q, want 0, %q and no stderr", code, stdout, stderr, versionString()+"\n")
	}
}

func TestUsageErrors(t *testing.T) {
	valid := []string{"sync", "--endpoint", "127.0.0.1:4445", "--dest", "d", "--state-dir", "s"}
	for _, tc := range []struct {
		args         []string
		option, rule string
	}{
		{[]string{"--bogus"}, "--bogus", `"unknown flag"`},
		{[]string{"-x"}, "-x", `"unknown flag"`},
		{[]string{"extra"}, "arguments", `"positional arguments are not allowed"`},
		{[]string{"licenses", "extra"}, "arguments", `"positional arguments are not allowed"`},
		{[]string{"licenses", "--version"}, "--version", `"unknown flag"`},
		{[]string{"sync", "--bogus"}, "--bogus", `"unknown flag"`},
		{[]string{"sync", "extra", "--endpoint", "h:445", "--dest", "d"}, "arguments", `"positional arguments are not allowed"`},
		{[]string{"sync"}, "--endpoint", `"required; give the SMB host:port"`},
		{[]string{"sync", "--dest", "d"}, "--endpoint", `"required; give the SMB host:port"`},
		{[]string{"sync", "--endpoint", "h:445"}, "--dest", `"required; give the local mirror folder"`},
		{[]string{"sync", "--endpoint", "h:445", "--dest", ""}, "--dest", `"required; give the local mirror folder"`},
		{[]string{"sync", "--dest", "d", "--endpoint"}, "--endpoint", `"requires a value"`},
		{slices.Concat(valid, []string{"--interval", "soon"}), "--interval", `"use a duration such as 30s, 5m or 1h"`},
		{slices.Concat(valid, []string{"--interval", "0s"}), "--interval", `"must be more than zero"`},
		{slices.Concat(valid, []string{"--interval", "-1s"}), "--interval", `"must be more than zero"`},
		{slices.Concat(valid, []string{"--ready-age", "-1ns"}), "--ready-age", `"must not be negative"`},
		{slices.Concat(valid, []string{"--first-pass-age", "-1m"}), "--first-pass-age", `"must not be negative"`},
		{slices.Concat(valid, []string{"--workers", "0"}), "--workers", `"must be 1 to 64"`},
		{slices.Concat(valid, []string{"--workers", "65"}), "--workers", `"must be 1 to 64"`},
		{slices.Concat(valid, []string{"--workers", "-1"}), "--workers", `"must be 1 to 64"`},
		{slices.Concat(valid, []string{"--workers", "four"}), "--workers", `"use a whole number"`},
		{slices.Concat(valid, []string{"--once=maybe"}), "--once", `"use true or false"`},
		{slices.Concat(valid, []string{"--dry-run", "--interval", "1m"}), "--dry-run", `"runs one pass; do not give --interval with it"`},
		{slices.Concat(valid, []string{"--state-dir", ""}), "--state-dir", `"must not be empty; omit it for the default"`},
	} {
		code, stdout, stderr := invoke(t, tc.args...)
		want := "option=" + tc.option + " rule=" + tc.rule + " help=\""
		if code != exitUsage || stdout != "" || !strings.Contains(stderr, want) || strings.Count(stderr, "\n") != 1 {
			t.Errorf("%q: code=%d stdout=%q stderr=%q, want 2, no stdout and one line with %q", tc.args, code, stdout, stderr, want)
		}
	}
}

func TestUsageErrorNamesHelp(t *testing.T) {
	_, _, stderr := invoke(t, "sync")
	if !strings.Contains(stderr, `level=ERROR msg="invalid arguments" `) || !strings.HasSuffix(stderr, ` help="seestar-copy-cli sync --help"`+"\n") {
		t.Errorf("stderr=%q, want an ERROR line ending with the sync help command", stderr)
	}
	_, _, stderr = invoke(t, "--bogus")
	if !strings.HasSuffix(stderr, ` help="seestar-copy-cli --help"`+"\n") {
		t.Errorf("stderr=%q, want the root help command", stderr)
	}
}

// --source, from 0.1.0, is a usage error that points to --exclude, in any
// form, and so stops before anything is dialed.
func TestSourceRemoved(t *testing.T) {
	const want = `option=--source rule="removed; sync copies the whole share, so leave folders out with --exclude"`
	for _, args := range [][]string{{"--source", "MyWorks"}, {"--source=MyWorks"}, {"--source"}} {
		code, _, stderr := invoke(t, slices.Concat([]string{"sync", "--endpoint", "h:445"}, scratch(t), args)...)
		if code != exitUsage || !strings.Contains(stderr, want) {
			t.Errorf("%q: code=%d stderr=%q, want 2 with %q", args, code, stderr, want)
		}
	}
	// Elsewhere it is just an unknown flag.
	code, _, stderr := invoke(t, "--source", "MyWorks")
	if code != exitUsage || !strings.Contains(stderr, `option=--source rule="unknown flag"`) {
		t.Errorf("top-level --source: code=%d stderr=%q, want 2 and unknown flag", code, stderr)
	}
}

func TestUTCLog(t *testing.T) {
	var b bytes.Buffer
	r := slog.NewRecord(time.Date(2026, 10, 10, 12, 34, 56, 123456789, time.FixedZone("offset", 3600)), slog.LevelInfo, "event", 0)
	if err := diagnostic.Logger("text", &b, slog.LevelInfo).Handler().Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if got, want := b.String(), "time=2026-10-10T11:34:56.123Z level=INFO msg=event\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The CLI's logs are byte for byte what its own handler wrote before it
// shared diagnostic's: text lines, UTC millisecond times, INFO and up.
func TestLogUnchanged(t *testing.T) {
	old := func(w io.Writer) *slog.Logger {
		return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey && len(groups) == 0 {
					return slog.String(slog.TimeKey, a.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z"))
				}
				return a
			},
		}))
	}
	at := time.Date(2026, 10, 10, 12, 34, 56, 123456789, time.FixedZone("offset", -7*3600))
	write := func(l *slog.Logger) {
		for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
			r := slog.NewRecord(at, level, "sync failed", 0)
			r.AddAttrs(slog.String("path", "MyWorks/M 31/a b.fit"), slog.Any("error", errors.New(`refused: "x"`)),
				slog.Int64("bytes", 1<<40), slog.Duration("elapsed", 1500*time.Millisecond), slog.Bool("once", true))
			if l.Enabled(context.Background(), level) {
				if err := l.Handler().Handle(context.Background(), r); err != nil {
					t.Fatal(err)
				}
			}
		}
		r := slog.NewRecord(at, slog.LevelWarn, "adopted", 0)
		r.AddAttrs(slog.String("folder", "MyWorks"))
		if err := l.With("run", 3).Handler().Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	var want, got bytes.Buffer
	write(old(&want))
	write(diagnostic.Logger("text", &got, slog.LevelInfo))
	if got.String() != want.String() || strings.Count(got.String(), "\n") != 4 {
		t.Errorf("got\n%s\nwant\n%s", &got, &want)
	}
}
