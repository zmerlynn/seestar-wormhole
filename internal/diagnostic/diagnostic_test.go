package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestSecretSentinels(t *testing.T) {
	const secrets = "email-sentinel password-sentinel token-sentinel serial-sentinel source-sentinel license-sentinel"
	for _, err := range []error{errors.New(secrets), &os.PathError{Op: secrets, Path: secrets, Err: syscall.EACCES}, fmt.Errorf("%s: %w", secrets, cloud.ErrUnauthorized)} {
		var b bytes.Buffer
		Log(slog.New(slog.NewJSONHandler(&b, nil)), "failed", "login", err)
		for _, secret := range strings.Fields(secrets) {
			if strings.Contains(b.String(), secret) {
				t.Fatal("secret escaped")
			}
		}
	}
}

func TestVerbosityFiltering(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		for _, level := range []string{"info", "debug", "warn", "error"} {
			verbosity, err := Level(level)
			if err != nil {
				t.Fatal(err)
			}
			var b bytes.Buffer
			l := Logger(format, &b, verbosity)
			done := Operation(l, "login")
			done(nil)
			l.Error("fatal")
			if !strings.Contains(b.String(), "fatal") {
				t.Fatal("fatal suppressed")
			}
			if strings.Contains(b.String(), "phase_transition") != (level == "debug") {
				t.Fatal("wrong verbosity")
			}
		}
	}
	if _, err := Level("secret-sentinel"); err == nil || strings.Contains(err.Error(), "secret-sentinel") {
		t.Fatal("unsafe invalid level")
	}
}

func TestFinalErrorReportsCleanupIndependently(t *testing.T) {
	for _, reported := range []bool{false, true} {
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		primary := NewTerminal(session.RDT, "timeout", session.ErrTransport)
		if reported {
			Log(logger, "layer_failed", "session", primary)
			logs.Reset()
		}
		cleanup := errors.Join(context.Canceled, session.ErrStop)
		LogFinal(logger, "proxy failed", "session", errors.Join(primary, cleanup), cleanup)
		var fields struct {
			Phase, Reason string
			CleanupReason string `json:"cleanup_reason"`
		}
		if err := json.Unmarshal(logs.Bytes(), &fields, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		wantPhase, wantReason := "rdt", "timeout"
		if reported {
			wantPhase, wantReason = "release", "release_failed"
		}
		if fields.Phase != wantPhase || fields.Reason != wantReason || fields.CleanupReason != "release_failed" {
			t.Fatalf("final diagnostic=%+v", fields)
		}
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	LogFinal(logger, "proxy failed", "arguments", errors.New("coordinate-time-credential-UID-payload-sentinel"), nil)
	if strings.Contains(logs.String(), "sentinel") || !strings.Contains(logs.String(), `"cleanup_reason":"none"`) {
		t.Fatalf("unsafe or incomplete final diagnostic: %s", &logs)
	}
}

func TestOperationReportingBoundary(t *testing.T) {
	for _, nested := range []bool{false, true} {
		var logs bytes.Buffer
		logger := Logger("json", &logs, slog.LevelDebug)
		if nested {
			logger = Lifecycle(logger).With("port", 445).WithGroup("operation")
		}
		finish := Operation(logger, "release")
		finish(errors.New("credential-coordinate-time-UID-payload-sentinel"))
		count := strings.Count(logs.String(), `"level":"ERROR"`)
		want := 1
		if nested {
			want = 0
		}
		if count != want || strings.Contains(logs.String(), "sentinel") {
			t.Fatalf("nested=%v: report boundary=%s", nested, &logs)
		}
		if nested && !strings.Contains(logs.String(), `"state":"failed"`) {
			t.Fatal("missing nested lifecycle failure")
		}
	}
}
