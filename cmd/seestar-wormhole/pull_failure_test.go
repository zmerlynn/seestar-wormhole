package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func assertPullFailureOutput(t *testing.T, out, logs string, reason string) {
	t.Helper()
	if !strings.Contains(out, "reason="+reason+" ") || !strings.Contains(out, "cleanup_reason=none") {
		t.Fatalf("summary lacks reason %s: %s", reason, out)
	}
	var record struct{ Reason, Action string }
	if err := json.Unmarshal([]byte(logs), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatalf("diagnostic: %v; %s", err, logs)
	}
	if record.Reason != reason || record.Action == "" {
		t.Fatalf("diagnostic=%+v want %s and action", record, reason)
	}
	for _, private := range []string{"PRIVATE_", "small.fit", "SMALL.fit", "/tmp/smb-e2e", "fixture-source"} {
		if strings.Contains(out+logs, private) {
			t.Fatalf("output leaked %s", private)
		}
	}
}

func TestPullFailureResultProjection(t *testing.T) {
	for _, tc := range []struct {
		cause  error
		reason string
		code   int
	}{
		{&diagnostic.PullError{Reason: diagnostic.FutureMtime}, "future_mtime", 1},
		{&diagnostic.PullError{Reason: diagnostic.InvalidMetadata}, "invalid_metadata", 1},
		{&diagnostic.PullError{Reason: diagnostic.CaseCollision}, "case_collision", 1},
		{&diagnostic.PullError{Reason: diagnostic.InventoryLimit}, "inventory_limit", 1},
		{&diagnostic.PullError{Reason: diagnostic.MetadataLimit}, "metadata_limit", 1},
		{&diagnostic.PullError{Reason: diagnostic.VerifyMismatch}, "verify_mismatch", 1},
		{&diagnostic.PullError{Reason: diagnostic.RemoteChanged}, "remote_changed", 1},
		{&diagnostic.PullError{Reason: diagnostic.DiskFull}, "disk_full", 1},
		{&diagnostic.PullError{Reason: diagnostic.UnsafePath}, "unsafe_path", 1},
		{&diagnostic.PullError{Reason: diagnostic.StateInvalid}, "state_invalid", 1},
		{&diagnostic.PullError{Reason: diagnostic.SourceMismatch}, "source_mismatch", 2},
		{session.ErrBusy, "busy", 3},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			if typed, ok := tc.cause.(*diagnostic.PullError); ok {
				typed.Cause = errors.New("PRIVATE_FILE PRIVATE_SERIAL PRIVATE_PATH")
			}
			// Internal worker cancellation must not hide the independent safety failure.
			cause := errors.Join(tc.cause, context.Canceled)
			var out, logs bytes.Buffer
			err := runPull(context.Background(), []string{"pull", t.TempDir()}, &out, &logs, keylog.Open,
				func(context.Context, proxy.Config) (commandSession, error) { return nil, cause })
			if pullCode(err) != tc.code {
				t.Fatalf("code=%d want %d", pullCode(err), tc.code)
			}
			assertPullFailureOutput(t, out.String(), logs.String(), tc.reason)
		})
	}
}

func TestPullSMBFailureReasons(t *testing.T) {
	addr := os.Getenv("SEESTAR_TEST_SAMBA_ADDR")
	if addr == "" {
		t.Skip("container Samba fixture required")
	}
	name := "/tmp/smb-e2e/share/MyWorks/nested/small.fit"
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{"future_mtime", "case_collision"} {
		t.Run(reason, func(t *testing.T) {
			if reason == "future_mtime" {
				future := time.Now().Add(time.Hour)
				if err := os.Chtimes(name, future, future); err != nil {
					t.Fatal(err)
				}
				defer os.Chtimes(name, info.ModTime(), info.ModTime())
			} else {
				collision := filepath.Join(filepath.Dir(name), "SMALL.fit")
				if err := os.WriteFile(collision, []byte("collision"), 0644); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(collision)
			}
			var out, logs bytes.Buffer
			s := &pullSessionFixture{addr: addr}
			err := runPull(context.Background(), []string{"pull", "--only=^nested$", "--settle=0", t.TempDir()}, &out, &logs, keylog.Open,
				func(context.Context, proxy.Config) (commandSession, error) { return s, nil })
			if pullCode(err) != 1 || s.closed != 1 {
				t.Fatalf("code=%d close=%d", pullCode(err), s.closed)
			}
			assertPullFailureOutput(t, out.String(), logs.String(), reason)
		})
	}
}

func TestPullInvalidRegexpIndex(t *testing.T) {
	var out, logs bytes.Buffer
	err := runPull(context.Background(), []string{"pull", "--only=valid", "--only=PRIVATE_[", "PRIVATE_DEST"}, &out, &logs, keylog.Open,
		func(context.Context, proxy.Config) (commandSession, error) {
			t.Fatal("invalid regexp opened session")
			return nil, nil
		})
	if pullCode(err) != 2 || !strings.Contains(logs.String(), "expression 2") || strings.Contains(logs.String()+out.String(), "PRIVATE_") {
		t.Fatalf("code=%d out=%s logs=%s", pullCode(err), &out, &logs)
	}
}

func TestPullFinalGraphRequiresDebug(t *testing.T) {
	for _, level := range []string{"info", "debug"} {
		t.Run(level, func(t *testing.T) {
			cause := errors.Join(context.DeadlineExceeded, io.EOF)
			for range 16 {
				cause = fmt.Errorf("PRIVATE_BACKEND: %w", cause)
			}
			var out, logs bytes.Buffer
			err := runPull(context.Background(), []string{"pull", "--log-level=" + level, t.TempDir()}, &out, &logs, keylog.Open,
				func(context.Context, proxy.Config) (commandSession, error) { return nil, cause })
			if pullCode(err) != 1 {
				t.Fatalf("exit=%d want 1", pullCode(err))
			}
			if !strings.Contains(logs.String(), "leaf_causes") || !strings.Contains(logs.String(), "peer_closed") || strings.Contains(logs.String(), "PRIVATE_BACKEND") {
				t.Fatalf("final=%s", &logs)
			}
			graph := strings.Contains(logs.String(), `"causes"`)
			if graph != (level == "debug") {
				t.Fatalf("graph at %s: %s", level, &logs)
			}
		})
	}
}
