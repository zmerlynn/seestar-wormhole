package buildinfo

import (
	"runtime"
	"runtime/debug"
	"testing"
)

func TestVersion(t *testing.T) {
	if got := Version(""); got != "development" {
		t.Errorf("Version(\"\") = %q, want development", got)
	}
	if got := Version("v1.2.3"); got != "v1.2.3" {
		t.Errorf("Version(v1.2.3) = %q", got)
	}
}

// A revision set at link time wins; otherwise the VCS stamp, shortened to
// 12 characters; otherwise "development".
func TestRevision(t *testing.T) {
	stamped := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
	}}
	short := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abc"}}}
	empty := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: ""}}}
	for _, tc := range []struct {
		name, linked string
		info         *debug.BuildInfo
		want         string
	}{
		{"link time wins", "fedcba987654", stamped, "fedcba987654"},
		{"vcs stamp shortened", "", stamped, "0123456789ab"},
		{"short vcs stamp", "", short, "abc"},
		{"empty vcs stamp", "", empty, "development"},
		{"no stamp", "", &debug.BuildInfo{}, "development"},
		{"no build info", "", nil, "development"},
	} {
		if got := revision(tc.linked, tc.info); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A release build's line is exactly the program, the linked version and
// revision, and the platform.
func TestString(t *testing.T) {
	want := "seestar-wormhole v1.2.3 (fedcba987654, " + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if got := String("seestar-wormhole", "v1.2.3", "fedcba987654"); got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
	if got, want := format("p", "development", "development", "linux", "arm64"), "p development (development, linux/arm64)"; got != want {
		t.Errorf("format = %q, want %q", got, want)
	}
}
