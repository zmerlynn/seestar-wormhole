package state

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPathLiteral(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture is a Unix path")
	}
	got, err := Path("/var/state", "/data/mirror")
	if err != nil {
		t.Fatal(err)
	}
	// printf %s /data/mirror | sha256sum
	want := "/var/state/sync/dbd1f26a06df151561bc85e15509b73bfa7b40b143fd0875a0b555f59d0e3d87.jsonl"
	if got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestPathStableAcrossForms(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	dest := filepath.Join(root, "dest")
	want, err := Path(stateDir, dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	sep := string(filepath.Separator)
	for _, form := range []string{
		dest + sep,
		dest + sep + "." + sep,
		filepath.Join(root, "x") + sep + ".." + sep + "dest",
		"dest",
		"." + sep + "dest" + sep,
	} {
		got, err := Path(stateDir, form)
		if err != nil {
			t.Errorf("Path(%q): %v", form, err)
		} else if got != want {
			t.Errorf("Path(%q) = %q, want %q", form, got, want)
		}
	}
	// A relative state directory resolves too.
	if got, err := Path("state", dest); err != nil || got != want {
		t.Errorf("Path(\"state\", dest) = %q, %v, want %q", got, err, want)
	}
	if other, _ := Path(stateDir, filepath.Join(root, "dest2")); other == want {
		t.Errorf("Path of another destination = %q, the same as dest", other)
	}
}

func TestPathRejects(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "dest")
	for _, tc := range []struct {
		name, stateDir, dest, want string
	}{
		{"empty state dir", "", dest, "state directory is empty"},
		{"empty dest", root, "", "destination is empty"},
		{"state dir is dest", dest, dest, "is inside the destination"},
		{"state dir in dest", filepath.Join(dest, "sub"), dest, "is inside the destination"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Path(tc.stateDir, tc.dest)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Path = %q, %v, want an error containing %q", got, err, tc.want)
			}
		})
	}
	// A sibling whose name starts with the destination's is outside it.
	if _, err := Path(dest+"-state", dest); err != nil {
		t.Errorf("Path with a sibling state dir: %v, want success", err)
	}
}

// TestAncestorSameAsIncludesCandidate checks that the identity walk compares
// the candidate itself, not only its parents: a candidate that is the
// destination's folder under another name has no matching ancestor.
func TestAncestorSameAsIncludesCandidate(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "dest")
	mkdir(t, dest)
	mkdir(t, filepath.Join(root, "other"))
	target, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, p, want string) {
		t.Helper()
		got, err := ancestorSameAs(p, target)
		if err != nil || got != want {
			t.Errorf("ancestorSameAs(%s) = %q, %v, want %q", p, got, err, want)
		}
	}
	check(t, dest, dest)
	check(t, filepath.Join(dest, "missing", "state"), dest)
	check(t, filepath.Join(root, "other"), "")
	check(t, filepath.Join(root, "other", "missing"), "")
	t.Run("link", func(t *testing.T) {
		link := filepath.Join(root, "link")
		symlink(t, dest, link)
		check(t, link, link)
		check(t, filepath.Join(link, "missing"), link)
	})
}
