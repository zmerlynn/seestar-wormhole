package pull

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// Config is the supported pull policy, deliberately smaller than rclone's CLI.
type Config struct {
	Destination, Since, Until                   string
	Only, Dirs                                  []string
	Settle, Overlap                             time.Duration
	Transfers                                   int
	BytesPerSecond                              int64
	Sums, ErrorOnEmpty, Recheck, Verify, DryRun bool
	// CancelOnPanic cancels the owning transport before joining workers whose
	// backend I/O may ignore their per-operation context.
	CancelOnPanic context.CancelFunc
	PlanLogger    *slog.Logger
	// Progress is optional command accounting; nil leaves accounting unchanged.
	Progress *Progress
}

// DefaultConfig returns the documented CLI defaults.
func DefaultConfig() Config {
	return Config{Since: "auto", Settle: 2 * time.Minute, Overlap: 5 * time.Minute, Transfers: 4, Sums: true}
}

func option(name, rule string) error { return &diagnostic.OptionError{Option: name, Rule: rule} }

// Validate checks policy before any resources or device are opened.
func (c *Config) Validate(goos string) error {
	if err := validateDestination(c.Destination, goos); err != nil {
		return option("destination", "use a local directory; Windows drive paths must be absolute")
	}
	if c.Settle < 0 {
		return option("--settle", "use a nonnegative Go duration")
	}
	if c.Overlap < 0 {
		return option("--overlap", "use a nonnegative Go duration")
	}
	if c.Transfers < 1 || c.Transfers > 8 {
		return option("--transfers", "use an integer from 1 through 8")
	}
	if c.BytesPerSecond < 0 {
		return option("--bwlimit", "use 0 or a positive bytes/s, KiB/s or MiB/s rate")
	}
	if _, err := c.bounds(time.Now().UTC(), time.Time{}); err != nil {
		return err
	}
	if err := validateDirectories(c.Dirs); err != nil {
		return err
	}
	c.Dirs = slices.Clone(c.Dirs)
	slices.Sort(c.Dirs)
	if err := validatePatterns(c.Only); err != nil {
		return err
	}
	c.Only = slices.Clone(c.Only)
	slices.Sort(c.Only)
	c.Only = slices.Compact(c.Only)
	return nil
}

// Bounds are inclusive SMB modification-time instants, never filename dates.
type Bounds struct{ Lower, Upper time.Time }

func (c Config) bounds(now, checkpoint time.Time) (Bounds, error) {
	b := Bounds{Upper: now.Add(-c.Settle).UTC()}
	switch c.Since {
	case "all":
	case "auto":
		if !checkpoint.IsZero() {
			b.Lower = checkpoint.Add(-c.Overlap).UTC()
		}
	default:
		instant, err := parseBound(c.Since, now)
		if err != nil {
			return b, option("--since", "use auto, all, zoned RFC3339 or a positive Go duration (no d unit)")
		}
		b.Lower = instant
	}
	if c.Until != "" {
		until, err := parseBound(c.Until, now)
		if err != nil {
			return b, option("--until", "use zoned RFC3339 or a positive Go duration (no d unit)")
		}
		if until.Before(b.Upper) {
			b.Upper = until
		}
	}
	return b, nil
}

// parseBound uses one run-start instant for duration-ago and zoned timestamps.
func parseBound(value string, now time.Time) (time.Time, error) {
	if age, err := time.ParseDuration(value); err == nil && age > 0 {
		return now.Add(-age).UTC(), nil
	}
	instant, err := time.Parse(time.RFC3339Nano, value)
	return instant.UTC(), err
}

func compilePatterns(patterns []string) ([]*regexp.Regexp, error) {
	expressions := make([]*regexp.Regexp, 0, len(patterns))
	for i, p := range patterns {
		expression, err := regexp.Compile(p)
		if err != nil {
			return nil, option("--only", fmt.Sprintf("expression %d must be a valid Go regexp (RE2)", i+1))
		}
		expressions = append(expressions, expression)
	}
	return expressions, nil
}

func validatePatterns(patterns []string) error {
	_, err := compilePatterns(patterns)
	return err
}

func matches(expressions []*regexp.Regexp, folder string) bool {
	if len(expressions) == 0 {
		return true
	}
	for _, expression := range expressions {
		if expression.MatchString(folder) {
			return true
		}
	}
	return false
}

func validateDestination(s, goos string) error {
	if s == "" || strings.IndexByte(s, 0) >= 0 {
		return errors.New("invalid destination")
	}
	if goos == "windows" {
		if len(s) >= 2 && s[1] == ':' && (len(s) < 3 || (s[2] != '\\' && s[2] != '/')) {
			return errors.New("drive-relative destination")
		}
		// Colon is legal only for a native drive prefix, never an rclone remote/ADS.
		rest := s
		if len(rest) >= 2 && rest[1] == ':' {
			rest = rest[2:]
		}
		if strings.Contains(rest, ":") {
			return errors.New("invalid destination colon")
		}
	}
	return nil
}

// validateRemote applies the same portable filename policy on every host.
func validateRemote(s string) error {
	if s == "" || path.IsAbs(s) || path.Clean(s) != s || strings.ContainsAny(s, `\<>:"|?*`) {
		return failure(diagnostic.UnsafePath)
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return failure(diagnostic.UnsafePath)
		}
	}
	parts := strings.Split(s, "/")
	if strings.EqualFold(parts[0], ".seestar-wormhole") || strings.EqualFold(parts[0], "SHA256SUMS") {
		return failure(diagnostic.UnsafePath)
	}
	for _, p := range parts {
		if strings.Contains(p, ".seestar-part-") || strings.HasPrefix(p, ".seestar-sums-") {
			return failure(diagnostic.UnsafePath)
		}
		if p == "." || p == ".." || strings.HasSuffix(p, ".") || strings.HasSuffix(p, " ") {
			return failure(diagnostic.UnsafePath)
		}
		stem := strings.ToUpper(strings.SplitN(p, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || stem == "CONIN$" || stem == "CONOUT$" {
			return failure(diagnostic.UnsafePath)
		}
		if (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && strings.Contains("123456789¹²³", strings.TrimPrefix(strings.TrimPrefix(stem, "COM"), "LPT")) && len([]rune(stem)) == 4 {
			return failure(diagnostic.UnsafePath)
		}
	}
	return nil
}

// validateListedRemote adds immediate directory membership to the shared path
// policy, before a backend or walker can normalize or discard malformed names.
func validateListedRemote(dir, name string) error {
	if err := validateRemote(name); err != nil {
		return err
	}
	parent := path.Dir(name)
	if (dir == "" && parent != ".") || (dir != "" && parent != dir) {
		return failure(diagnostic.UnsafePath)
	}
	return nil
}

// validateDirectories rejects ambiguous destinations before opening any resources.
func validateDirectories(dirs []string) error {
	names := map[string]string{}
	for _, dir := range dirs {
		if err := validateRemote(dir); err != nil {
			return option("--dir", "use portable share-relative paths with forward slashes; no absolute paths, traversal, empty segments, backslashes or reserved names")
		}
		parts := strings.Split(dir, "/")
		for i := range parts {
			p := strings.Join(parts[:i+1], "/")
			key := folded(p)
			if old, ok := names[key]; ok && old != p {
				return option("--dir", "directory paths must not collide by case or Unicode normalization")
			}
			names[key] = p
		}
	}
	listed := map[string]bool{}
	for _, dir := range dirs {
		key := folded(dir)
		if listed[key] {
			return option("--dir", "directories must be distinct and must not overlap")
		}
		listed[key] = true
	}
	for _, dir := range dirs {
		for parent := path.Dir(dir); parent != "."; parent = path.Dir(parent) {
			if listed[folded(parent)] {
				return option("--dir", "directories must be distinct and must not overlap")
			}
		}
	}
	return nil
}
