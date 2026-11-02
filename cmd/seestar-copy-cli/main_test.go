package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"
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
		for _, want := range []string{"Usage:\n  seestar-copy-cli [flags]\n", "  sync ", "  licenses ", "--version"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("%q: help lacks %q:\n%s", args, want, stdout)
			}
		}
		if !strings.HasSuffix(stdout, "Third-party license information: seestar-copy-cli licenses\n") {
			t.Errorf("%q: help lacks the final license pointer:\n%s", args, stdout)
		}
	}
}

func TestSyncHelp(t *testing.T) {
	code, stdout, stderr := invoke(t, "sync", "--help")
	if code != exitOK || stderr != "" {
		t.Fatalf("code=%d stderr=%q, want 0 and no stderr", code, stderr)
	}
	for _, want := range []string{
		"--endpoint string", "--dest string", "--source stringArray",
		"any replaces the defaults (default [MyWorks,Autorun/Dark,darklibrary])",
		"--interval duration", "(default 5m0s)", "--once", "--dry-run", "(default 2m0s)", "(default 10m0s)",
		"files copied at once (1-64) (default 4)", "--state-dir string",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("sync help lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Third-party license information") {
		t.Error("the license pointer belongs to the root help only")
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

func TestFormatVersion(t *testing.T) {
	stamped := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
	}}
	short := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}}}
	for _, tc := range []struct {
		name, version, revision string
		info                    *debug.BuildInfo
		want                    string
	}{
		{"link time wins", "v1.2.3", "fedcba987654", stamped, "seestar-copy-cli v1.2.3 (fedcba987654, linux/arm64)"},
		{"vcs stamp shortened", "development", "", stamped, "seestar-copy-cli development (0123456789ab, linux/arm64)"},
		{"short vcs stamp", "development", "", short, "seestar-copy-cli development (abc, linux/arm64)"},
		{"no stamp", "development", "", &debug.BuildInfo{}, "seestar-copy-cli development (development, linux/arm64)"},
		{"no build info", "development", "", nil, "seestar-copy-cli development (development, linux/arm64)"},
	} {
		if got := formatVersion(tc.version, tc.revision, tc.info, "linux", "arm64"); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
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

func TestEndpoint(t *testing.T) {
	for _, endpoint := range []string{"h", "h:", ":445", "h:0", "h:65536", "h:smb", "h:-1", "h:+445", "::1:445", "h:445:1", "[::1]"} {
		code, _, stderr := invoke(t, "sync", "--endpoint", endpoint, "--dest", "d", "--state-dir", "s")
		if code != exitUsage || !strings.Contains(stderr, `option=--endpoint rule="use host:port, such as 127.0.0.1:4445"`) {
			t.Errorf("%q: code=%d stderr=%q, want 2 naming --endpoint", endpoint, code, stderr)
		}
	}
	for _, endpoint := range []string{"127.0.0.1:4445", "[::1]:445", "seestar.local:445", "h:1", "h:65535"} {
		code, _, stderr := invoke(t, slices.Concat([]string{"sync", "--endpoint", endpoint}, scratch(t))...)
		if code != exitFailure || !strings.Contains(stderr, "connect to "+endpoint+": "+errNoDevice.Error()) {
			t.Errorf("%q: code=%d stderr=%q, want accepted and the stubbed connect failure", endpoint, code, stderr)
		}
	}
}

func TestSources(t *testing.T) {
	const rule = `option=--source rule="use a folder relative to the share root, with forward slashes, such as MyWorks"`
	const overlap = `option=--source rule="sources must not repeat or contain each other"`
	for _, tc := range []struct {
		sources []string
		want    string
	}{
		{[]string{""}, rule}, {[]string{"."}, rule}, {[]string{"/MyWorks"}, rule}, {[]string{"../x"}, rule},
		{[]string{"a/../b"}, rule}, {[]string{"a//b"}, rule}, {[]string{"a/"}, rule}, {[]string{`a\b`}, rule},
		{[]string{"C:/x"}, rule}, {[]string{"C:"}, rule}, {[]string{"a/b:c"}, rule},
		{[]string{"MyWorks", "MyWorks"}, overlap},
		{[]string{"MyWorks", "MyWorks/M 31"}, overlap},
		{[]string{"Autorun/Dark", "Autorun"}, overlap},
		{[]string{"MyWorks", "MyWorksOld"}, ""},
		{[]string{"My,Works"}, ""},
	} {
		args := slices.Concat([]string{"sync", "--endpoint", "h:445"}, scratch(t))
		for _, s := range tc.sources {
			args = append(args, "--source", s)
		}
		code, _, stderr := invoke(t, args...)
		if tc.want == "" {
			if code != exitFailure || !strings.Contains(stderr, errNoDevice.Error()) {
				t.Errorf("%q: code=%d stderr=%q, want accepted and exit 1", tc.sources, code, stderr)
			}
		} else if code != exitUsage || !strings.Contains(stderr, tc.want) {
			t.Errorf("%q: code=%d stderr=%q, want 2 with %q", tc.sources, code, stderr, tc.want)
		}
	}
}

func TestSyncFlags(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		sources []string
	}{
		{nil, []string{"MyWorks", "Autorun/Dark", "darklibrary"}},
		{[]string{"--source", "MyWorks"}, []string{"MyWorks"}},
		{[]string{"--source", "b", "--source", "a,c"}, []string{"b", "a,c"}},
	} {
		cmd := newSyncCommand(newLogger(io.Discard))
		if err := cmd.ParseFlags(tc.args); err != nil {
			t.Fatal(err)
		}
		f := cmd.Flags()
		sources, _ := f.GetStringArray("source")
		if !slices.Equal(sources, tc.sources) {
			t.Errorf("%q: sources %q, want %q", tc.args, sources, tc.sources)
		}
		if len(tc.args) > 0 {
			continue
		}
		for name, want := range map[string]time.Duration{"interval": 5 * time.Minute, "ready-age": 2 * time.Minute, "first-pass-age": 10 * time.Minute} {
			if got, _ := f.GetDuration(name); got != want {
				t.Errorf("--%s default %v, want %v", name, got, want)
			}
		}
		if got, _ := f.GetInt("workers"); got != 4 {
			t.Errorf("--workers default %d, want 4", got)
		}
		for name, want := range map[string]string{"endpoint": "", "dest": "", "state-dir": ""} {
			if got, _ := f.GetString(name); got != want {
				t.Errorf("--%s default %q, want %q", name, got, want)
			}
		}
		if got, _ := f.GetBool("once"); got {
			t.Error("--once is on by default")
		}
		if got, _ := f.GetBool("dry-run"); got {
			t.Error("--dry-run is on by default")
		}
	}
}

func TestValidDefaults(t *testing.T) {
	o := syncOptions{endpoint: "h:445", dest: "d", sources: defaultSources, interval: 5 * time.Minute,
		readyAge: 0, firstPassAge: 0, workers: 1}
	if err := o.validate(); err != nil {
		t.Errorf("zero ages and one worker: %v", err)
	}
}

// The bounds are literal: the help and the error both promise 1 to 64.
func TestWorkerBounds(t *testing.T) {
	for _, tc := range []struct {
		workers string
		code    int
		want    string
	}{
		{"1", exitFailure, errNoDevice.Error()},
		{"64", exitFailure, errNoDevice.Error()},
		{"0", exitUsage, `option=--workers rule="must be 1 to 64"`},
		{"65", exitUsage, `option=--workers rule="must be 1 to 64"`},
	} {
		code, stdout, stderr := invoke(t, slices.Concat([]string{"sync", "--endpoint", "h:445", "--workers", tc.workers}, scratch(t))...)
		if code != tc.code || stdout != "" || !strings.Contains(stderr, tc.want) {
			t.Errorf("--workers %s: code=%d stdout=%q stderr=%q, want %d with %q", tc.workers, code, stdout, stderr, tc.code, tc.want)
		}
	}
}

func TestStateDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("HOME", base)
	t.Setenv("AppData", base)
	want, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	want = filepath.Join(want, "seestar-copy")
	got, err := (&syncOptions{}).resolveStateDir()
	if err != nil || got != want {
		t.Errorf("default state dir %q, %v, want %q", got, err, want)
	}
	got, err = (&syncOptions{stateDir: "x", stateDirSet: true}).resolveStateDir()
	if err != nil || got != "x" {
		t.Errorf("explicit state dir %q, %v, want x", got, err)
	}
}

func TestUTCLog(t *testing.T) {
	var b bytes.Buffer
	r := slog.NewRecord(time.Date(2026, 10, 10, 12, 34, 56, 123456789, time.FixedZone("offset", 3600)), slog.LevelInfo, "event", 0)
	if err := newLogger(&b).Handler().Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if got, want := b.String(), "time=2026-10-10T11:34:56.123Z level=INFO msg=event\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
