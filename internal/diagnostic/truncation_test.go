package diagnostic

import (
	"bytes"
	"errors"
	"github.com/go-json-experiment/json"
	"io"
	"log/slog"
	"slices"
	"testing"
)

func TestDiagnosticTruncationRequirement(t *testing.T) {
	errs := make([]error, 64)
	for i := range errs {
		errs[i] = io.EOF
	}
	errs[63] = errors.ErrUnsupported
	var logs bytes.Buffer
	LogFinal(Logger("json", &logs, slog.LevelInfo), "proxy failed", "session", errors.Join(errs...), nil)
	var record struct {
		Leaves []string `json:"leaf_causes"`
	}
	if err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(record.Leaves, "unsupported") && !slices.Contains(record.Leaves, "more") {
		t.Fatalf("omitted distinct leaf without truncation marker: %s", &logs)
	}
}
