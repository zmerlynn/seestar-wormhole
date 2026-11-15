package diagnostic

import "log/slog"

// A Fallback is a saved setting that a load replaced because its value
// was invalid: the file, the setting's name, what it fell back to and the
// value it replaced.
type Fallback struct{ File, Setting, Fallback, Value string }

// LogFallbacks writes one warning for each fallback, with its value clipped.
func LogFallbacks(l *slog.Logger, fallbacks []Fallback) {
	for _, f := range fallbacks {
		attrs := []any{"file", f.File, "setting", f.Setting, "fallback", f.Fallback}
		if f.Value != "" {
			attrs = append(attrs, "value", Clip(f.Value, MaxValue))
		}
		l.Warn("settings_fallback", attrs...)
	}
}
