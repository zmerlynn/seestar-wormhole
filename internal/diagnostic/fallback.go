package diagnostic

import "log/slog"

// A Fallback is a saved setting that a load replaced because its value
// was invalid: the file, the setting's name and what it fell back to. It
// never holds the value it replaced, which may be private.
type Fallback struct{ File, Setting, Fallback string }

// LogFallbacks writes one warning for each fallback.
func LogFallbacks(l *slog.Logger, fallbacks []Fallback) {
	for _, f := range fallbacks {
		l.Warn("settings_fallback", "file", f.File, "setting", f.Setting, "fallback", f.Fallback)
	}
}
