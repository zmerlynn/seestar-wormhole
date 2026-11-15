package ui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A name is refused only for what can't safely be one folder's name, and
// for default; anything else is kept as typed.
func TestCheckProfileName(t *testing.T) {
	for _, tc := range []struct{ name, message string }{
		{"", "Enter a name."},
		{strings.Repeat("x", 65), "This name is too long. Use a shorter one."},
		{"default", "Default already exists. Choose another name."},
		{"DEFAULT", "Default already exists. Choose another name."},
		{".", "A name can't start with a dot."},
		{"..", "A name can't start with a dot."},
		{".Garden", "A name can't start with a dot."},
		{"Gar/den", `A name can't contain / or \.`},
		{`Gar\den`, `A name can't contain / or \.`},
		{"Gar\x00den", "A name can't contain control characters."},
		{"Gar\nden", "A name can't contain control characters."},
		{"Gar\x7fden", "A name can't contain control characters."},
	} {
		err := CheckProfileName(tc.name)
		if refused, ok := errors.AsType[*ProfileNameError](err); !ok || refused.Message != tc.message {
			t.Errorf("%q: %v, want %q", tc.name, err, tc.message)
		}
	}
	for _, name := range []string{"Garden", "TEST0002", "Back yard S50", "Garten (Süd)", "a.b", strings.Repeat("x", 64), "défault"} {
		if err := CheckProfileName(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
}

// A created profile is a private folder under the root's profiles folder,
// named as typed; a name differing only by case is the same profile.
func TestCreateAndFindProfile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if names, err := Profiles(root); err != nil || names != nil {
		t.Errorf("no root: %q, %v", names, err)
	}
	name, err := CreateProfile(root, " Garden ")
	dir := filepath.Join(root, "profiles", "Garden")
	if err != nil || name != "Garden" || ProfileDir(root, name) != dir {
		t.Fatalf("= %q, %v", name, err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("no folder: %v", err)
	}
	for _, name := range []string{"Garden", "GARDEN", "garden "} {
		if _, err := CreateProfile(root, name); err == nil || err.Error() != "There's already a profile named Garden." {
			t.Errorf("%s again: %v", name, err)
		}
	}
	if _, err := CreateProfile(root, "Default"); err == nil || err.Error() != "Default already exists. Choose another name." {
		t.Errorf("created default: %v", err)
	}
	// A folder that can't be made says why, in its real words.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProfile(blocked, "Garden"); err == nil || !strings.HasPrefix(err.Error(), "Couldn't create the profile: ") || !strings.Contains(err.Error(), blocked) {
		t.Errorf("under a file: %v", err)
	}
	if _, err := CreateProfile(root, "TEST0002"); err != nil {
		t.Fatal(err)
	}
	// A file, or a folder no profile could be named, is not a profile.
	if err := os.WriteFile(filepath.Join(root, "profiles", "notes"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "profiles", ".hidden"), 0o700); err != nil {
		t.Fatal(err)
	}
	if names, err := Profiles(root); err != nil || !slices.Equal(names, []string{"Garden", "TEST0002"}) {
		t.Errorf("Profiles = %q, %v", names, err)
	}

	for _, tc := range []struct{ name, dir, found string }{
		{"default", root, "default"},
		{"Default", root, "default"},
		{"Garden", dir, "Garden"},
		{"gARDEN", dir, "Garden"},
	} {
		if d, found, err := FindProfile(root, tc.name); err != nil || d != tc.dir || found != tc.found {
			t.Errorf("%s = %q %q, %v", tc.name, d, found, err)
		}
	}
	_, _, err = FindProfile(root, "Orchard")
	if missing, ok := errors.AsType[*NoProfileError](err); !ok || missing.Error() != "no profile named Orchard; profiles: default, Garden, TEST0002" {
		t.Errorf("Orchard: %v", err)
	}
}
