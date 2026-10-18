package pull

import (
	"context"
	"log/slog"
	"strings"
)

// BackendLogHandler routes backend refusals and retries through the command's
// logger. Backend text, attributes and object names can contain private paths
// or credentials; only fixed categories cross this boundary.
func BackendLogHandler(logger *slog.Logger) slog.Handler { return backendLogHandler{logger} }

type backendLogHandler struct{ logger *slog.Logger }

func (h backendLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h backendLogHandler) Handle(ctx context.Context, r slog.Record) error {
	event := "backend_warning"
	if strings.Contains(strings.ToLower(r.Message), "retry") {
		event = "backend_retry"
	} else if r.Level < slog.LevelWarn {
		return nil
	}
	record := slog.NewRecord(r.Time, slog.LevelWarn, event, r.PC)
	record.Add("rule", "backend_diagnostic")
	return h.logger.Handler().Handle(ctx, record)
}
func (h backendLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h backendLogHandler) WithGroup(string) slog.Handler      { return h }
