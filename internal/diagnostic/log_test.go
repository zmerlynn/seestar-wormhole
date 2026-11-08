package diagnostic

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The file commands' text logs are byte for byte what the old seestar-copy-cli's
// own handler wrote before it shared this one: text lines, UTC millisecond
// times, INFO and up.
func TestLogUnchanged(t *testing.T) {
	old := func(w io.Writer) *slog.Logger {
		return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey && len(groups) == 0 {
					return slog.String(slog.TimeKey, a.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z"))
				}
				return a
			},
		}))
	}
	at := time.Date(2026, 10, 10, 12, 34, 56, 123456789, time.FixedZone("offset", -7*3600))
	write := func(l *slog.Logger) {
		for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
			r := slog.NewRecord(at, level, "sync failed", 0)
			r.AddAttrs(slog.String("path", "MyWorks/M 31/a b.fit"), slog.Any("error", errors.New(`refused: "x"`)),
				slog.Int64("bytes", 1<<40), slog.Duration("elapsed", 1500*time.Millisecond), slog.Bool("once", true))
			if l.Enabled(context.Background(), level) {
				if err := l.Handler().Handle(context.Background(), r); err != nil {
					t.Fatal(err)
				}
			}
		}
		r := slog.NewRecord(at, slog.LevelWarn, "adopted", 0)
		r.AddAttrs(slog.String("folder", "MyWorks"))
		if err := l.With("run", 3).Handler().Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	var want, got bytes.Buffer
	write(old(&want))
	write(Logger("text", &got, slog.LevelInfo))
	if got.String() != want.String() || strings.Count(got.String(), "\n") != 4 {
		t.Errorf("got\n%s\nwant\n%s", &got, &want)
	}
}
