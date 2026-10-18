package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestFinalCompactRendering(t *testing.T) {
	for _, message := range []string{"proxy failed"} {
		for _, format := range []string{"json", "text"} {
			for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
				t.Run(fmt.Sprintf("%s/%s/%s", message, format, level), func(t *testing.T) {
					var logs bytes.Buffer
					err := errors.Join(context.DeadlineExceeded, io.EOF, privateCause{}, io.EOF)
					for range 16 {
						err = fmt.Errorf("PRIVATE_PATH: %w", err)
					}
					LogFinal(Logger(format, &logs, level), message, "session", err, nil)
					lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
					want := 1
					if level == slog.LevelDebug {
						want = 2
					}
					if len(lines) != want {
						t.Fatalf("lines=%d want=%d: %s", len(lines), want, &logs)
					}
					final := lines[len(lines)-1]
					if len(final) > 400 || !strings.Contains(final, "timeout") || !strings.Contains(final, "peer_closed") || !strings.Contains(final, "operation_failed") || !strings.Contains(final, "leaf_causes") || strings.Contains(final, "parent") || strings.Contains(logs.String(), "PRIVATE_PATH") {
						t.Fatalf("default diagnostic too long, missing causes, or private: %s", &logs)
					}
					if format == "json" {
						var r map[string]any
						if err := json.Unmarshal([]byte(final), &r, json.MatchCaseInsensitiveNames(true)); err != nil {
							t.Fatal(err)
						}
						if r["msg"] != message || r["reason"] != "timeout" || r["level"] != "ERROR" || r["causes"] != nil {
							t.Fatalf("final=%v", r)
						}
						want := []string{"timeout", "peer_closed", "operation_failed"}
						got := r["leaf_causes"].([]any)
						if len(got) != len(want) {
							t.Fatalf("leaf causes=%v", got)
						}
						for i := range want {
							if got[i] != want[i] {
								t.Fatalf("leaf causes=%v want=%v", got, want)
							}
						}
						if level == slog.LevelDebug {
							var debug struct {
								Level  string  `json:"level"`
								Causes []Cause `json:"causes"`
							}
							if err := json.Unmarshal([]byte(lines[0]), &debug, json.MatchCaseInsensitiveNames(true)); err != nil {
								t.Fatal(err)
							}
							if debug.Level != "DEBUG" || len(debug.Causes) != 21 || debug.Causes[20].Reason != "peer_closed" || debug.Causes[20].Parent != 16 {
								t.Fatalf("graph=%+v", debug)
							}
						}
					}
				})
			}
		}
	}
}

func TestFinalLeafSummaryBounded(t *testing.T) {
	var logs bytes.Buffer
	err := errors.Join(context.DeadlineExceeded, context.Canceled, io.EOF, net.ErrClosed,
		os.ErrPermission, os.ErrNotExist, syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.EPIPE,
		session.ErrBudget, session.ErrBusy, session.ErrStop)
	LogFinal(Logger("json", &logs, slog.LevelInfo), "proxy failed", "session", err, nil)
	var record struct {
		Leaves []string `json:"leaf_causes"`
	}
	if err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	if len(record.Leaves) != 9 || record.Leaves[8] != "more" || logs.Len() > 550 {
		t.Fatalf("unbounded summary=%s", &logs)
	}
}
