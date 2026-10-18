package diagnostic

import (
	"errors"
	"io"
	"log/slog"
)

// Level accepts only the documented verbosity names.
func Level(value string) (slog.Level, error) {
	switch value {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, errors.New("invalid log level")
	}
}

// Logger uses UTC millisecond timestamps and a fixed configured threshold.
func Logger(format string, w io.Writer, level slog.Level) *slog.Logger {
	options := &slog.HandlerOptions{Level: level, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			return slog.String(slog.TimeKey, a.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z"))
		}
		return a
	}}
	if format == "text" {
		return slog.New(slog.NewTextHandler(w, options))
	}
	return slog.New(slog.NewJSONHandler(w, options))
}

// Operation logs edge lifecycle boundaries. Lifecycle marks a composed
// owner's nested operations; standalone operations keep default-visible failures.
// Call the returned completion exactly once. Phase must be a constant label.
func Operation(l *slog.Logger, phase string) func(error) {
	if l != nil {
		l.Debug("phase_transition", "phase", phase, "state", "started")
	}
	return func(err error) {
		if l == nil {
			return
		}
		if err != nil {
			if _, nested := l.Handler().(lifecycleHandler); nested {
				operation, reason := Fields(err, phase)
				l.Debug("phase_transition", "phase", operation, "reason", reason, "state", "failed")
			} else {
				Log(l, "phase_failed", phase, err)
			}
			return
		}
		l.Debug("phase_transition", "phase", phase, "state", "ready")
	}
}

// Lifecycle marks a logger whose final failures are reported by the enclosing
// owner. It changes Operation failures into debug transitions and leaves other
// records alone. Standalone internal loggers retain their ordinary error reporting.
func Lifecycle(l *slog.Logger) *slog.Logger {
	if l == nil {
		return nil
	}
	if _, ok := l.Handler().(lifecycleHandler); ok {
		return l
	}
	return slog.New(lifecycleHandler{l.Handler()})
}

type lifecycleHandler struct{ slog.Handler }

func (h lifecycleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return lifecycleHandler{h.Handler.WithAttrs(attrs)}
}
func (h lifecycleHandler) WithGroup(name string) slog.Handler {
	return lifecycleHandler{h.Handler.WithGroup(name)}
}
