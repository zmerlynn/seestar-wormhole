package diagnostic

import (
	"bytes"
	"context"
	"encoding/json/v2"
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

// Clip keeps a short value, and cuts a long one on a UTF-8 boundary with a
// marker naming its length.
func TestClip(t *testing.T) {
	for _, tc := range []struct {
		s    string
		n    int
		want string
	}{
		{"", 4, ""},
		{"abcd", 4, "abcd"},
		{"abcde", 4, "abcd…(truncated, 5 bytes)"},
		{"ab€d", 3, "ab…(truncated, 6 bytes)"},
		{"ab€d", 5, "ab€…(truncated, 6 bytes)"},
	} {
		if got := Clip(tc.s, tc.n); got != tc.want {
			t.Errorf("Clip(%q, %d) = %q; want %q", tc.s, tc.n, got, tc.want)
		}
	}
}

// An oversized option name or value is clipped, with its prefix and a
// marker, in a record a log sink takes in either format.
func TestOptionErrorClips(t *testing.T) {
	long := strings.Repeat("<", 20000)
	for _, e := range []*OptionError{
		{Option: "--zzz" + long, Rule: "unknown flag"},
		{Option: "--bind", Rule: "use an OS-supported bind address", Value: "192.0.2." + long},
	} {
		for _, format := range []string{"json", "text"} {
			var b bytes.Buffer
			Logger(format, &b, slog.LevelInfo).Error("proxy failed", e.Attrs()...)
			key, prefix := "option", "--zzz<<<"
			if e.Value != "" {
				key, prefix = "value", "192.0.2.<<<"
			}
			var field string
			if format == "json" {
				var record map[string]any
				_ = json.Unmarshal(b.Bytes(), &record)
				field, _ = record[key].(string)
			} else {
				_, field, _ = strings.Cut(b.String(), key+"=")
			}
			if b.Len() >= 16<<10 || !strings.Contains(field, prefix) || !strings.Contains(field, "…(truncated, 200") {
				t.Errorf("%s record of %d bytes: %.300s", format, b.Len(), b.String())
			}
			if msg := e.Error(); len(msg) >= 2*MaxValue+200 || !strings.Contains(msg, prefix) {
				t.Errorf("Error() of %d bytes: %.300s", len(msg), msg)
			}
		}
	}
}
