package pull

import (
	"strings"
	"testing"
	"time"
)

func TestSelectionBounds(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, value := range []string{"auto", "all", "24h", "2026-10-12T07:00:00.125-05:00"} {
		c := DefaultConfig()
		c.Since = value
		if _, err := c.bounds(now, time.Time{}); err != nil {
			t.Errorf("%s: %v", value, err)
		}
	}
	for _, value := range []string{"2026-10-12", "2026-10-12T07:00:00", "-1h", "0", "1d"} {
		c := DefaultConfig()
		c.Since = value
		if _, err := c.bounds(now, time.Time{}); err == nil {
			t.Errorf("accepted %s", value)
		}
	}
	c := DefaultConfig()
	c.Overlap = 0
	c.Settle = 10 * time.Minute
	checkpoint := now.Add(-time.Hour)
	b, err := c.bounds(now, checkpoint)
	if err != nil || !b.Lower.Equal(checkpoint) || !b.Upper.Equal(now.Add(-10*time.Minute)) {
		t.Fatalf("bounds=%+v err=%v", b, err)
	}
}

func TestFolderRegexps(t *testing.T) {
	for _, pattern := range []string{"M 31", "^M 31$", `M 31 \(wide\)`, "", "a/b", `a\\b`} {
		if err := validatePatterns([]string{pattern}); err != nil {
			t.Errorf("%q: %v", pattern, err)
		}
	}
	for _, pattern := range []string{"[", "**", "("} {
		err := validatePatterns([]string{"valid", pattern})
		if err == nil || !strings.Contains(err.Error(), "expression 2") {
			t.Errorf("%q: want expression 2 error, got %v", pattern, err)
		}
	}
	for _, tc := range []struct {
		patterns []string
		folder   string
		want     bool
	}{
		{nil, "anything", true},
		{[]string{"M 31"}, "M 31", true},
		{[]string{"M 31"}, "M 31_sub", true},
		{[]string{"M 31"}, "prefix M 31 suffix", true},
		{[]string{"^M 31$"}, "M 31", true},
		{[]string{"^M 31$"}, "M 31_sub", false},
		{[]string{"m 31"}, "M 31", false},
		{[]string{`M 31 \(wide\)`}, "M 31 (wide)_sub", true},
		{[]string{"Dark.*", "^M 31$"}, "M 31", true},
		{[]string{"Dark.*", "^M 31$"}, "Light", false},
		{[]string{"Dark*"}, "Dark_sub", true},
		{[]string{"Dark*"}, "Dar", true}, // RE2 quantifier, no glob fallback.
	} {
		expressions, err := compilePatterns(tc.patterns)
		if err != nil {
			t.Fatal(err)
		}
		if got := matches(expressions, tc.folder); got != tc.want {
			t.Errorf("%q / %q: got %t want %t", tc.patterns, tc.folder, got, tc.want)
		}
	}
	c := DefaultConfig()
	c.Destination = "."
	c.Only = []string{"M 31", "Dark.*", "M 31"}
	if err := c.Validate("linux"); err != nil {
		t.Fatal(err)
	}
	first := selectionKey(c.Only)
	c.Only = []string{"Dark.*", "M 31"}
	if err := c.Validate("linux"); err != nil {
		t.Fatal(err)
	}
	if selectionKey(c.Only) != first {
		t.Fatal("order/duplicates changed expression set key")
	}
	c.Only = []string{"^M 31$", "Dark.*"}
	if selectionKey(c.Only) == first {
		t.Fatal("changed expression set reused key")
	}
}

func TestPortablePaths(t *testing.T) {
	for _, name := range []string{"Dark 7 LP_sub/frame.fit", "Ω/a.fit", "nested/" + string(make([]byte, 0)) + "a.fit"} {
		if err := validateRemote(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"../a", "/a", "a/../b", "a//b", `a\b`, "a/NUL.fit", "COM1/x", "a:ads", "a/trail.", "a/trail ", ".seestar-wormhole/a", "SHA256SUMS", "a/\nfit", "a/LPT².fit"} {
		if err := validateRemote(name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	for _, name := range []string{`C:relative`, `C:`, `D:folder\x`} {
		if err := validateDestination(name, "windows"); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
	for _, name := range []string{`C:\Astro Data`, `\\server\share\Astro Data`, `relative\Astro Data`} {
		if err := validateDestination(name, "windows"); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
}
