package pull

import (
	"bytes"
	"context"
	"github.com/rclone/rclone/fs"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestBackendWarnings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		level   slog.Level
		message string
		want    bool
	}{
		{"warning", slog.LevelWarn, "PRIVATE_PATH warning", true},
		{"error", slog.LevelError, "PRIVATE_CREDENTIAL failure", true},
		{"retry", slog.LevelDebug, "Received error PRIVATE_PATH - low level retry 1/3", true},
		{"notice", fs.SlogLevelNotice, "PRIVATE_PATH notice", false},
		{"detail", slog.LevelDebug, "PRIVATE_PATH metadata", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			h := BackendLogHandler(slog.New(slog.NewJSONHandler(&logs, nil)))
			fs.SetLogger(h.WithAttrs([]slog.Attr{slog.String("object", "PRIVATE_OBJECT")}).WithGroup("PRIVATE_GROUP"))
			fs.LogPrint(fs.LogLevelDebug, nil, "Received error PRIVATE_PATH - low level retry 1/3")
			logs.Reset()
			r := slog.NewRecord(time.Time{}, tc.level, tc.message, 0)
			r.Add("error", "PRIVATE_ERROR")
			if err := h.Handle(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			got := logs.String()
			if tc.want != strings.Contains(got, `"rule":"backend_diagnostic"`) || (tc.want && !strings.Contains(got, `"level":"WARN"`)) || strings.Contains(got, "PRIVATE") {
				t.Fatalf("unexpected backend diagnostic: %s", got)
			}
		})
	}
}
