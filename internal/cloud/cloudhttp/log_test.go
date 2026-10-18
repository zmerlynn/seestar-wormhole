package cloudhttp

import (
	"bytes"
	"io"
	"log/slog"
	"testing"
	"time"
)

// redactionTestLogger keeps whole-event sentinel scans independent of clock
// digits. Preserve every other attribute, including times inside groups.
func redactionTestLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}

func TestRedactionLoggerPreservesNonTimeAttributes(t *testing.T) {
	var sink bytes.Buffer
	logger := redactionTestLogger(&sink, slog.LevelDebug)
	record := slog.NewRecord(time.Date(2026, 10, 10, 0, 0, 0, 987654321, time.UTC), slog.LevelDebug, "probe", 0)
	record.AddAttrs(slog.Int("cloud_code", 9876), slog.Group("shape", slog.Int("time", 87654321)))
	if err := logger.Handler().Handle(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	want := `{"level":"DEBUG","msg":"probe","cloud_code":9876,"shape":{"time":87654321}}` + "\n"
	if got := sink.String(); got != want {
		t.Fatalf("log = %s, want %s", got, want)
	}
}
