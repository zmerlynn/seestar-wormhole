package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type shortStartupSink struct{}

func (shortStartupSink) Write(p []byte) (int, error) { return len(p) / 2, nil }

func TestStartupDiagnosticLoss(t *testing.T) {
	for _, name := range []string{"cap", "denied", "flush", "keylog_replaced", "keylog_denied", "keylog_flush"} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			s := newStartupLogging(options{format: "json", level: "info"}, &logs)
			var receipt error
			switch name {
			case "cap":
				s.output.Write(bytes.Repeat([]byte("x"), startupLogLimit))
				_, receipt = s.output.Write([]byte("lost"))
			case "denied":
				s.output.resolve(false)
				_, receipt = s.output.Write([]byte("lost"))
			case "flush":
				s.output.output = shortStartupSink{}
				s.output.Write([]byte("lost"))
				receipt = s.output.resolve(true)
				s.output.output = &logs
			default:
				h := &startupKeylogHandler{Handler: slog.NewJSONHandler(&logs, nil)}
				r := slog.NewRecord(time.Time{}, slog.LevelWarn, "keylog_failed", 0)
				h.Handle(context.Background(), r)
				if name == "keylog_replaced" {
					receipt = h.Handle(context.Background(), r)
				}
				if name == "keylog_flush" {
					h.Handler = slog.NewJSONHandler(&startupLogOutput{output: shortStartupSink{}, resolved: true, accepted: true}, nil)
				}
				receipt2 := h.resolve(name != "keylog_denied")
				if receipt == nil {
					receipt = receipt2
				}
				s.handler = h
			}
			err := s.finish(nil)
			if receipt == nil || err == nil {
				t.Fatalf("lost diagnostic returned success: receipt=%v final=%v", receipt, err)
			}
			if !strings.Contains(logs.String(), `"level":"WARN"`) || !strings.Contains(logs.String(), `"rule":"startup_diagnostics_lost"`) || !strings.Contains(logs.String(), `"count":`) {
				t.Fatalf("missing loss WARN: %s", &logs)
			}
		})
	}
}
