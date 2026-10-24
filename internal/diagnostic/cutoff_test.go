package diagnostic

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// Nil children are legal for arbitrary multi-error implementations, even
// though errors.Join filters them out.
type cutoffJoin []error

func (cutoffJoin) Error() string     { panic("private error formatted") }
func (e cutoffJoin) Unwrap() []error { return e }

func TestFinalDiagnosticCutoff(t *testing.T) {
	repeated := func(n int) []error {
		errs := make([]error, n)
		for i := range errs {
			errs[i] = io.EOF
		}
		return errs
	}
	wrapped := func(n int) error {
		err := errors.ErrUnsupported
		for range n {
			err = fmt.Errorf("PRIVATE_WRAPPER: %w", err)
		}
		return err
	}
	eight := []error{context.DeadlineExceeded, context.Canceled, io.EOF, net.ErrClosed,
		os.ErrPermission, os.ErrNotExist, syscall.ECONNREFUSED, syscall.ECONNRESET}
	tests := []struct {
		name   string
		err    error
		nodes  int
		leaves []string
	}{
		{"below cutoff", errors.Join(repeated(62)...), 63, []string{"peer_closed"}},
		{"exact cutoff", errors.Join(repeated(63)...), 64, []string{"peer_closed"}},
		{"repeated labels truncated", errors.Join(repeated(64)...), 64, []string{"peer_closed", "more"}},
		{"nil at cutoff", cutoffJoin(append(repeated(63), nil, nil)), 64, []string{"peer_closed"}},
		{"distinct after nil at cutoff", cutoffJoin(append(repeated(63), nil, errors.ErrUnsupported)), 64, []string{"peer_closed", "more"}},
		{"wrapped exact cutoff", wrapped(63), 64, []string{"unsupported"}},
		{"wrapped just beyond cutoff", wrapped(64), 64, []string{"more"}},
		{"deep wrapping", wrapped(128), 64, []string{"more"}},
		{"retained leaf and cutoff wrapper", errors.Join(io.EOF, wrapped(64)), 64, []string{"peer_closed", "more"}},
		{"label and node limits", errors.Join(append(append(eight, repeated(55)...), errors.ErrUnsupported)...), 64,
			[]string{"timeout", "canceled", "peer_closed", "connection_closed", "permission_denied", "not_found", "connection_refused", "connection_reset", "more"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(Causes(tt.err)); got != tt.nodes {
				t.Fatalf("graph nodes=%d want=%d", got, tt.nodes)
			}
			for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
				var logs bytes.Buffer
				LogFinal(Logger("json", &logs, level), "proxy failed", "session", tt.err, nil)
				lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
				wantLines := 1
				if level == slog.LevelDebug {
					wantLines = 2
				}
				if len(lines) != wantLines || strings.Contains(logs.String(), "PRIVATE_WRAPPER") {
					t.Fatalf("level=%s unexpected output: %s", level, &logs)
				}
				var record struct {
					Leaves []string `json:"leaf_causes"`
				}
				if err := json.Unmarshal(lines[len(lines)-1], &record, json.MatchCaseInsensitiveNames(true)); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(record.Leaves, tt.leaves) {
					t.Fatalf("level=%s leaf_causes=%v want=%v", level, record.Leaves, tt.leaves)
				}
				if level == slog.LevelDebug {
					var debug struct {
						Causes []Cause `json:"causes"`
					}
					if err := json.Unmarshal(lines[0], &debug, json.MatchCaseInsensitiveNames(true)); err != nil {
						t.Fatal(err)
					}
					if len(debug.Causes) != tt.nodes {
						t.Fatalf("DEBUG graph nodes=%d want=%d", len(debug.Causes), tt.nodes)
					}
				}
			}
		})
	}
}
