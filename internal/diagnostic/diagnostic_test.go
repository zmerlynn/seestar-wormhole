package diagnostic

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

// An error's text shows in full, but access secrets wrapped inside it still
// print as their markers.
func TestErrorTextAndSecretMarkers(t *testing.T) {
	credentials, err := cloud.EmailCredentials("me@example.com", "password-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	token, err := cloud.ParseLogin(200, []byte(`{"data":{"accessToken":"token-sentinel"}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	allocation, _, err := cloud.ParseOnline([]byte(`{"code":200,"data":{"conId":"connection-sentinel","deviceUid":"uid-sentinel","sdkLicenseKey":"license-sentinel","username":"user-sentinel","password":"relay-sentinel"}}`))
	if err != nil {
		t.Fatal(err)
	}
	wrapped := fmt.Errorf("login %v with %v for %v: %w", credentials, token, allocation,
		&os.PathError{Op: "open", Path: "/Users/me/Seestar/state.json", Err: syscall.EACCES})
	for _, final := range []bool{false, true} {
		var b bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&b, nil))
		if final {
			LogFinal(logger, "proxy failed", "login", wrapped, nil)
		} else {
			Log(logger, "failed", "login", wrapped)
		}
		var record struct{ Error string }
		if err := json.Unmarshal(b.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		if record.Error != wrapped.Error() || !strings.Contains(record.Error, "open /Users/me/Seestar/state.json: permission denied") {
			t.Fatalf("final=%v: error text missing: %s", final, &b)
		}
		for _, marker := range []string{"[account credentials]", "[account token]", "[cloud allocation]"} {
			if !strings.Contains(record.Error, marker) {
				t.Fatalf("final=%v: missing %s: %s", final, marker, &b)
			}
		}
		for _, secret := range []string{"password-sentinel", "token-sentinel", "connection-sentinel", "uid-sentinel", "license-sentinel", "user-sentinel", "relay-sentinel"} {
			if strings.Contains(b.String(), secret) {
				t.Fatalf("final=%v: %s escaped: %s", final, secret, &b)
			}
		}
	}
}

// A huge error is clipped, so the record stays well under a log sink's bound.
func TestErrorTextClipped(t *testing.T) {
	huge := errors.New(strings.Repeat("x", 64<<10) + "TAIL")
	for _, final := range []bool{false, true} {
		var b bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&b, nil))
		if final {
			LogFinal(logger, "proxy failed", "login", huge, nil)
		} else {
			Log(logger, "failed", "login", huge)
		}
		if b.Len() > 16<<10 || strings.Contains(b.String(), "TAIL") || !strings.Contains(b.String(), "xxx") {
			t.Fatalf("final=%v: record of %d bytes", final, b.Len())
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
	LogFinal(logger, "proxy failed", "arguments", errors.New("bad argument"), nil)
	if !strings.Contains(logs.String(), `"error":"bad argument"`) || !strings.Contains(logs.String(), `"cleanup_reason":"none"`) {
		t.Fatalf("incomplete final diagnostic: %s", &logs)
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
		finish(errors.New("release refused"))
		count := strings.Count(logs.String(), `"level":"ERROR"`)
		want := 1
		if nested {
			want = 0
		}
		if count != want || strings.Contains(logs.String(), "release refused") != !nested {
			t.Fatalf("nested=%v: report boundary=%s", nested, &logs)
		}
		if nested && !strings.Contains(logs.String(), `"state":"failed"`) {
			t.Fatal("missing nested lifecycle failure")
		}
	}
}
