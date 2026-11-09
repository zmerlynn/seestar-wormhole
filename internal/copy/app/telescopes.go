package app

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Telescope is one telescope's entry in copy.json's telescopes: the folder
// its images are copied to. Members a later version adds are kept.
type Telescope struct {
	Folder  string         `json:"folder"`
	Unknown jsontext.Value `json:",embed"`
}

// SerialKey is the telescopes key of the telescope with serial: the same
// on both routes, so a change of route keeps its folder.
func SerialKey(serial string) string { return "serial:" + serial }

// AddressKey is the telescopes key of an address on this network saved
// without a serial, host:port as saved.
func AddressKey(endpoint string) string { return "address:" + endpoint }

// claimed reports whether any telescope has a folder of its own. Until one
// does, as in a copy.json from before telescopes, the shared Folder is the
// folder of whichever telescope is first known: the saved one.
func (s Settings) claimed() bool {
	for _, t := range s.Telescopes {
		if t.Folder != "" {
			return true
		}
	}
	return false
}

// FolderFor is the folder of the telescope keyed key, or "" when it has
// none yet. Until a telescope has a folder of its own, every telescope's
// folder is Folder, which the first one known takes. A key of "" is a
// telescope not known yet.
func (s Settings) FolderFor(key string) string {
	if !s.claimed() {
		return s.Folder
	}
	if key == "" {
		return ""
	}
	return s.Telescopes[key].Folder
}

// withFolder is s with folder as the folder of the telescope keyed key.
// Folder, which an older app reads, follows the first telescope's: it
// changes when key's folder was Folder. A key of "" changes Folder alone,
// which only a telescope not known yet before any has a folder may do.
func (s Settings) withFolder(key, folder string) Settings {
	if key == "" {
		s.Folder = folder
		return s
	}
	if s.FolderFor(key) == s.Folder {
		s.Folder = folder
	}
	t := maps.Clone(s.Telescopes)
	if t == nil {
		t = map[string]Telescope{}
	}
	e := t[key]
	e.Folder = folder
	t[key] = e
	s.Telescopes = t
	return s
}

// claim is s with Folder given to the telescope keyed key when no
// telescope has a folder of its own yet, so a later telescope can't take
// it too; ok reports whether that changed s.
func (s Settings) claim(key string) (Settings, bool) {
	if key == "" || s.Folder == "" || s.claimed() {
		return s, false
	}
	return s.withFolder(key, s.Folder), true
}

// adopt is s with the folder of the telescope keyed from given to the one
// keyed to, which has none, and from's entry removed. The members a later
// version added to from's entry go with its folder, taking the place of
// to's of the same name.
func (s Settings) adopt(from, to string) (Settings, error) {
	t := maps.Clone(s.Telescopes)
	e := t[to]
	e.Folder = t[from].Folder
	unknown, err := mergeMembers(e.Unknown, t[from].Unknown)
	if err != nil {
		return s, err
	}
	e.Unknown = unknown
	t[to] = e
	delete(t, from)
	s.Telescopes = t
	return s, nil
}

// mergeMembers is the object of base's members and over's, over's winning
// a name both have, with its members in name order.
func mergeMembers(base, over jsontext.Value) (jsontext.Value, error) {
	if len(over) == 0 {
		return base, nil
	}
	if len(base) == 0 {
		return over, nil
	}
	var m, o map[string]jsontext.Value
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(over, &o); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]jsontext.Value{}
	}
	maps.Copy(m, o)
	b, err := json.Marshal(m, json.Deterministic(true))
	return jsontext.Value(b), err
}

// Messages for a folder another telescope's folder is, contains or is in.
// They name no folder and no telescope.
const (
	folderTaken    = "Another telescope copies to this folder. Choose a folder of its own for this telescope."
	folderInside   = "This folder is inside another telescope's folder. Choose a folder outside it."
	folderContains = "Another telescope's folder is inside this folder. Choose a folder that doesn't hold it."
)

// folderUnresolved is the message for a folder whose symbolic links don't
// resolve, so it can't be told apart from another telescope's.
const folderUnresolved = "This folder's links can't be followed, so it can't be checked against the other telescopes' folders. Choose another folder."

// overlap checks folder, for the telescope keyed key, against every other
// telescope's folder: it may be none of them, nor inside one, nor hold
// one, by its text or on disk, through a symbolic link or another
// spelling of a folder that exists. It returns the message for the first
// that clashes, or "".
func (s Settings) overlap(key, folder string) string {
	if !s.claimed() {
		return ""
	}
	for k, t := range s.Telescopes {
		if k == key || t.Folder == "" {
			continue
		}
		if msg := relate(folder, t.Folder); msg != "" {
			return msg
		}
	}
	return ""
}

// relate is the message for folder a against another telescope's folder
// b: the same folder, inside it or holding it, or "" for neither. Each is
// compared as written, with its symbolic links resolved, and by
// [os.SameFile] for the parts that exist. A link that doesn't resolve
// fails closed.
func relate(a, b string) string {
	if msg := relateText(a, b); msg != "" {
		return msg
	}
	ra, err := resolved(a)
	if err != nil {
		return folderUnresolved
	}
	rb, err := resolved(b)
	if err != nil {
		return folderUnresolved
	}
	if msg := relateText(ra, rb); msg != "" {
		return msg
	}
	switch inside(ra, rb) {
	case 0:
		return folderTaken
	case 1:
		return folderInside
	}
	if inside(rb, ra) == 1 {
		return folderContains
	}
	return ""
}

// relateText is relate by the text of a and b alone.
func relateText(a, b string) string {
	switch {
	case samePath(a, b):
		return folderTaken
	case under(a, b):
		return folderInside
	case under(b, a):
		return folderContains
	}
	return ""
}

// inside reports by [os.SameFile] whether the folder b, which must exist,
// is a or one of a's folders: 0 for a itself, 1 for a folder above it,
// and -1 for neither, or when b doesn't exist.
func inside(a, b string) int {
	want, err := os.Stat(b)
	if err != nil {
		return -1
	}
	depth := 0
	for d := filepath.Clean(a); ; depth++ {
		if fi, err := os.Stat(d); err == nil && os.SameFile(want, fi) {
			return min(depth, 1)
		}
		up := filepath.Dir(d)
		if up == d {
			return -1
		}
		d = up
	}
}

// resolved is the absolute folder p with the symbolic links of its
// deepest part that exists resolved, and the rest, which doesn't exist
// yet, as written. It fails for a link that doesn't resolve.
func resolved(p string) (string, error) {
	p = filepath.Clean(p)
	var rest []string
	for d := p; ; {
		_, err := os.Lstat(d)
		if err == nil {
			real, err := filepath.EvalSymlinks(d)
			if err != nil {
				return "", err
			}
			slices.Reverse(rest)
			return filepath.Join(append([]string{real}, rest...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		up := filepath.Dir(d)
		if up == d {
			return p, nil
		}
		rest, d = append(rest, filepath.Base(d)), up
	}
}

// others are the folders of every telescope but the one keyed key.
func (s Settings) others(key string) []string {
	var out []string
	if !s.claimed() {
		return out
	}
	for k, t := range s.Telescopes {
		if k != key && t.Folder != "" {
			out = append(out, t.Folder)
		}
	}
	return out
}

// foldCase reports whether paths compare without case, as the usual file
// systems of Windows and macOS do; tests replace it.
var foldCase = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// pathKey is p cleaned, and folded when paths compare without case.
func pathKey(p string) string {
	p = filepath.Clean(p)
	if foldCase {
		p = strings.ToLower(p)
	}
	return p
}

// samePath reports whether a and b name the same folder by their text.
func samePath(a, b string) bool { return pathKey(a) == pathKey(b) }

// under reports whether a is inside b by their text.
func under(a, b string) bool {
	a, b = pathKey(a), pathKey(b)
	if !strings.HasSuffix(b, string(filepath.Separator)) {
		b += string(filepath.Separator)
	}
	return strings.HasPrefix(a, b)
}

// Suggested is the folder suggested for a telescope that has none yet:
// beside first, the first telescope's folder, named name with the
// characters a folder name can't hold replaced.
func Suggested(first, name string) string {
	if first == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(filepath.Clean(first)), folderName(name))
}

// folderName is name made safe as one folder's name on every system:
// separators, the characters Windows forbids and control characters
// become "-", and trailing dots and spaces go.
func folderName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '-'
		}
		return r
	}, name)
	name = strings.TrimRight(name, ". ")
	if name == "" {
		return "Seestar"
	}
	return name
}
