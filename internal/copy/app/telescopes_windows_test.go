package app

import "testing"

// Windows spells one folder many ways: either separator, any case, a
// trailing separator, a drive's root and a UNC share's.
func TestOverlapWindowsPaths(t *testing.T) {
	s := *claimedAt(`C:\Images\A`, map[string]string{keyA: `C:\Images\A`})
	for _, p := range []string{`c:/images/a/`, `C:\Images\A\`, `C:\Images\A\child`, `C:\Images`, `C:\`} {
		if s.overlap(keyB, p) == "" {
			t.Errorf("accepted %s", p)
		}
	}
	if got := s.overlap(keyB, `D:\Images\A`); got != "" {
		t.Errorf("another drive: %q", got)
	}
	s = *claimedAt(`\\server\share\A`, map[string]string{keyA: `\\server\share\A`})
	for _, p := range []string{`//SERVER/share/A/`, `\\server\share\A\child`, `\\server\share`} {
		if s.overlap(keyB, p) == "" {
			t.Errorf("accepted UNC %s", p)
		}
	}
}
