package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// sharedBuffer is a buffer that a running command and the test share.
type sharedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *sharedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *sharedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// runFileCommand runs args through the program's exit path with signals
// from ctx, and returns its exit code, stdout and logs.
func runFileCommand(ctx context.Context, args []string, out, logs *sharedBuffer) int {
	return mainExitCodeWithPlatform(args, out, logs,
		func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
		func() (io.Writer, func()) { return logs, func() {} }, false)
}

// noReadOnlyGuard keeps the read-only guard off for the rest of t: in one
// process, a read-only command's guard would refuse every later write.
func noReadOnlyGuard(t *testing.T) {
	enable := enableReadOnly
	t.Cleanup(func() { enableReadOnly = enable })
	enableReadOnly = func() {}
}

// records decodes every log line of a run in format.
func records(t *testing.T, format, logs string) []map[string]any {
	t.Helper()
	var all []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(logs), "\n") {
		all = append(all, decodeRecord(t, format, line))
	}
	return all
}

// failureKeys are the keys a failure record gains.
var failureKeys = []string{"source", "op", "phase", "reason"}

// browseFailures runs browse against a closed loopback port, asks for one
// listing, stops it, and returns its logs.
func browseFailures(t *testing.T, format string) string {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, logs sharedBuffer
	done := make(chan int, 1)
	go func() {
		done <- runFileCommand(ctx, []string{"browse", "--endpoint", "127.0.0.1:1", "--log-format", format}, &out, &logs)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), "\n") {
		if time.Now().After(deadline) {
			t.Fatalf("browse printed no URL; logs %s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	page, _, _ := strings.Cut(strings.TrimSpace(out.String()), "#")
	resp, err := http.Get(strings.TrimSuffix(page, "/") + "/api/list?path=MyWorks")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("browse did not stop")
	}
	return logs.String()
}

// Every runtime failure record of a file command carries the classified
// source, op, phase and reason, in both formats, beside its raw error.
func TestFailureRecordsCarryKeys(t *testing.T) {
	noReadOnlyGuard(t)
	type want struct{ msg, source, op, phase, reason string }
	for _, format := range []string{"text", "json"} {
		for _, tc := range []struct {
			name  string
			args  func(t *testing.T) []string
			wants []want
		}{
			{"sync", func(t *testing.T) []string {
				return []string{"sync", "--once", "--endpoint", "127.0.0.1:1", "--dest", t.TempDir(), "--history-dir", t.TempDir()}
			}, []want{
				{"pass failed", "copy", "pass", "connect", "connection_refused"},
				{"sync failed", "copy", "run", "connect", "connection_refused"},
			}},
			{"sync dry run", func(t *testing.T) []string {
				return []string{"sync", "--dry-run", "--endpoint", "127.0.0.1:1", "--dest", t.TempDir(), "--history-dir", t.TempDir()}
			}, []want{
				{"dry run failed", "copy", "preview", "connect", "connection_refused"},
				{"sync failed", "copy", "run", "connect", "connection_refused"},
			}},
			{"find", func(t *testing.T) []string {
				return []string{"find", "--endpoint", "127.0.0.1:1"}
			}, []want{
				{"listing failed", "find", "list", "connect", "connection_refused"},
				{"find failed", "find", "run", "connect", "connection_refused"},
			}},
			{"delete preview", func(t *testing.T) []string {
				return []string{"delete", "--endpoint", "127.0.0.1:1", "--dest", t.TempDir()}
			}, []want{
				{"dry run failed", "delete", "preview", "connect", "connection_refused"},
				{"delete failed", "delete", "run", "connect", "connection_refused"},
			}},
			{"unwritable dest", func(t *testing.T) []string {
				file := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(file, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return []string{"sync", "--once", "--endpoint", "127.0.0.1:1", "--dest", filepath.Join(file, "dest"), "--history-dir", t.TempDir()}
			}, []want{
				{"sync failed", "copy", "run", "run", ""},
			}},
			{"browse", nil, []want{
				{"connect failed", "browse", "connect", "connect", "connection_refused"},
				{"request failed", "browse", "read", "connect", "connection_refused"},
			}},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				var logs string
				if tc.args == nil {
					logs = browseFailures(t, format)
				} else {
					var out, buf sharedBuffer
					if code := runFileCommand(context.Background(), append(tc.args(t), "--log-format", format), &out, &buf); code != 1 {
						t.Fatalf("exit %d; logs %s", code, buf.String())
					}
					logs = buf.String()
				}
				all := records(t, format, logs)
				for _, w := range tc.wants {
					i := slices.IndexFunc(all, func(r map[string]any) bool { return r["msg"] == w.msg })
					if i < 0 {
						t.Errorf("no %q record in %s", w.msg, logs)
						continue
					}
					r := all[i]
					if r["source"] != w.source || r["op"] != w.op || r["phase"] != w.phase || w.reason != "" && r["reason"] != w.reason ||
						r["reason"] == nil || r["reason"] == "" || r["error"] == nil || r["error"] == "" {
						t.Errorf("%q: %v, want source=%s op=%s phase=%s reason=%s and an error", w.msg, r, w.source, w.op, w.phase, w.reason)
					}
				}
				if format == "text" && tc.wants[0].source == "copy" && tc.wants[0].op == "pass" &&
					!strings.Contains(logs, `msg="pass failed" source=copy op=pass phase=connect`) {
					t.Errorf("the text pass record does not lead with its keys: %s", logs)
				}
			})
		}
	}
}

// The usage record keeps cli-merge's keys alone: an unknown flag on a file
// command gains no source and no op, whatever --log-format says.
func TestUsageRecordHasNoRuntimeKeys(t *testing.T) {
	noReadOnlyGuard(t)
	files := fileArgs(t)
	for _, name := range fileCommandNames {
		for _, extra := range [][]string{nil, {"--log-format=json"}} {
			args := append(append(slices.Clone(files[name]), extra...), "--synthetic-unknown")
			var out, logs sharedBuffer
			if code := runFileCommand(context.Background(), args, &out, &logs); code != 2 {
				t.Errorf("%q: exit %d", args, code)
			}
			all := records(t, "text", logs.String())
			if len(all) != 1 {
				t.Fatalf("%q: %d records: %s", args, len(all), logs.String())
			}
			var keys []string
			for k := range all[0] {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			want := []string{"help", "level", "msg", "option", "phase", "reason", "rule", "time"}
			if !slices.Equal(keys, want) || all[0]["phase"] != "arguments" || all[0]["reason"] != "invalid_option" {
				t.Errorf("%q: record %v, want keys %v with phase arguments and reason invalid_option", args, all[0], want)
			}
		}
	}
}

// phase and reason are classified, never built from an error's text: an
// endpoint and destination with sentinels reach only the error key.
func TestClassifiedKeysHoldNoNames(t *testing.T) {
	noReadOnlyGuard(t)
	file := filepath.Join(t.TempDir(), "FILE-SENTINEL")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "PATH-SENTINEL")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"sync", "--once", "--endpoint", "127.0.0.1:1", "--dest", filepath.Join(file, "PATH-SENTINEL"), "--history-dir", t.TempDir()},
		{"find", "--endpoint", "HOST-SENTINEL.invalid:445"},
		{"delete", "--endpoint", "HOST-SENTINEL.invalid:445", "--dest", dest},
	} {
		for _, format := range []string{"text", "json"} {
			var out, logs sharedBuffer
			runFileCommand(context.Background(), append(slices.Clone(args), "--log-format", format), &out, &logs)
			named := false
			for _, r := range records(t, format, logs.String()) {
				if r["source"] == nil {
					continue
				}
				for _, k := range failureKeys {
					if v, _ := r[k].(string); strings.Contains(v, "SENTINEL") {
						t.Errorf("%q %s: %s=%q names the input", args, format, k, v)
					}
				}
				if v, _ := r["error"].(string); strings.Contains(v, "SENTINEL") {
					named = true
				}
			}
			if !named {
				t.Errorf("%q %s: no failure record's error names the input: %s", args, format, logs.String())
			}
		}
	}
}
