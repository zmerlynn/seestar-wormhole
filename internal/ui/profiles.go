package ui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// DefaultProfile is the profile that is the state root itself. A named
// profile is a full state folder of its own under the root's profiles
// folder.
const DefaultProfile = "default"

// profilesDir is the root's folder of named profiles.
const profilesDir = "profiles"

// maxProfileName is the longest name, in bytes.
const maxProfileName = 64

// ProfileNameError is why a new profile's name was refused, as a plain
// sentence.
type ProfileNameError struct{ Message string }

func (e *ProfileNameError) Error() string { return e.Message }

// CheckProfileName checks a new profile's name: one folder name a person
// typed, already trimmed. It refuses only what can't safely be a folder's
// name, and default, which always exists. Windows drops a trailing dot or
// space from a folder's name, which would make the folder another name's.
func CheckProfileName(name string) error {
	refuse := func(message string) error { return &ProfileNameError{message} }
	switch {
	case name == "":
		return refuse("Enter a name.")
	case len(name) > maxProfileName:
		return refuse("This name is too long. Use a shorter one.")
	case sameProfile(name, DefaultProfile):
		return refuse("Default already exists. Choose another name.")
	case strings.HasPrefix(name, "."):
		return refuse("A name can't start with a dot.")
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " "):
		return refuse("A profile name can't end with a dot or a space.")
	case strings.ContainsAny(name, `/\`):
		return refuse(`A name can't contain / or \.`)
	case strings.ContainsFunc(name, unicode.IsControl):
		return refuse("A name can't contain control characters.")
	}
	return nil
}

// profileNameKey is what profile names are compared by, as macOS and
// Windows compare folder names: NFC, then Unicode case folding, then NFC
// again, as Copy compares the names of files. A name is kept as typed;
// the key is only for comparing.
func profileNameKey(name string) string {
	return norm.NFC.String(cases.Fold().String(norm.NFC.String(name)))
}

// sameProfile reports whether the names a and b are one profile's.
func sameProfile(a, b string) bool { return profileNameKey(a) == profileNameKey(b) }

// Profiles lists the root's named profiles as their folders are named,
// sorted by profileNameKey. A folder whose name isn't one a profile
// could have is not one. A root without profiles has none.
func Profiles(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, profilesDir))
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && CheckProfileName(e.Name()) == nil {
			names = append(names, e.Name())
		}
	}
	slices.SortFunc(names, func(a, b string) int {
		if c := strings.Compare(profileNameKey(a), profileNameKey(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return names, nil
}

// NoProfileError is a name with no profile, and the names there are.
type NoProfileError struct {
	Name     string
	Existing []string
}

func (e *NoProfileError) Error() string {
	return "no profile named " + e.Name + "; profiles: " + strings.Join(e.Existing, ", ")
}

// FindProfile is the state folder of the root's profile with that name,
// and its name as its folder is named: the root itself for default, else
// the named profile's folder, matched as macOS and Windows match folder
// names (see profileNameKey). A name with no folder is a *NoProfileError.
func FindProfile(root, name string) (dir, found string, err error) {
	if sameProfile(name, DefaultProfile) {
		return root, DefaultProfile, nil
	}
	names, err := Profiles(root)
	if err != nil {
		return "", "", err
	}
	for _, n := range names {
		if n == name {
			return ProfileDir(root, n), n, nil
		}
	}
	for _, n := range names {
		if sameProfile(n, name) {
			return ProfileDir(root, n), n, nil
		}
	}
	return "", "", &NoProfileError{Name: name, Existing: append([]string{DefaultProfile}, names...)}
}

// ProfileDir is the state folder of the root's profile name.
func ProfileDir(root, name string) string {
	if name == DefaultProfile {
		return root
	}
	return filepath.Join(root, profilesDir, name)
}

// CreateProfile creates the private state folder of a new profile named
// as typed, without leading or trailing spaces (Windows drops trailing
// ones, which would quietly make a second profile of the same name), and
// returns that name. A name that profileNameKey matches to an existing one
// is that one's. A refusal is a *ProfileNameError.
func CreateProfile(root, typed string) (string, error) {
	name := strings.TrimSpace(typed)
	if err := CheckProfileName(name); err != nil {
		return "", err
	}
	if _, found, err := FindProfile(root, name); err == nil {
		return "", &ProfileNameError{"There's already a profile named " + found + "."}
	} else if _, ok := errors.AsType[*NoProfileError](err); !ok {
		return "", &ProfileNameError{"Couldn't create the profile: " + err.Error() + "."}
	}
	dir := ProfileDir(root, name)
	if _, err := os.Lstat(dir); err == nil {
		return "", &ProfileNameError{"There's already a profile named " + name + "."}
	}
	for _, d := range []string{root, filepath.Dir(dir), dir} {
		f, err := privatefile.Directory(d)
		if err != nil {
			return "", &ProfileNameError{"Couldn't create the profile: " + err.Error() + "."}
		}
		f.Close()
	}
	return name, nil
}
