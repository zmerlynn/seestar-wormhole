package engine

import (
	"errors"
	"maps"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// selector parses excludes and includes into a Selector, failing the test
// on a bad pattern.
func selector(t *testing.T, excludes, includes []string) Selector {
	t.Helper()
	var s Selector
	for _, list := range []struct {
		flags []string
		into  *[]Pattern
	}{{excludes, &s.Excludes}, {includes, &s.Includes}} {
		for _, f := range list.flags {
			p, err := ParsePattern(f)
			if err != nil {
				t.Fatalf("%q: %v", f, err)
			}
			*list.into = append(*list.into, p)
		}
	}
	return s
}

// legacyPattern is the pattern matcher before .gitignore syntax: literal
// characters, * and ? only, through path.Match. It is the oracle for
// TestLegacyPatternsKeepTheirMeaning.
type legacyPattern struct {
	pattern  string
	anchored bool
}

func legacy(s string) legacyPattern {
	return legacyPattern{strings.ToLower(strings.TrimPrefix(s, "/")), strings.Contains(s, "/")}
}

func (m legacyPattern) match(p string) bool {
	if !m.anchored {
		p = path.Base(p)
	}
	ok, _ := path.Match(m.pattern, strings.ToLower(p))
	return ok
}

// matchWithin reports whether p, or a folder above it, matches.
func (m legacyPattern) matchWithin(p string) bool {
	for {
		if m.match(p) {
			return true
		}
		i := strings.LastIndexByte(p, '/')
		if i < 0 {
			return false
		}
		p = p[:i]
	}
}

func (m legacyPattern) mayMatchBelow(dir string) bool {
	if !m.anchored {
		return true
	}
	pat := strings.Split(m.pattern, "/")
	names := strings.Split(strings.ToLower(dir), "/")
	n := min(len(pat), len(names))
	ok, _ := path.Match(strings.Join(pat[:n], "/"), strings.Join(names[:n], "/"))
	return ok
}

// legacyCases are the pattern test cases from before .gitignore syntax,
// with the paths each was checked against.
var legacyCases = []struct {
	pattern string
	paths   []string
}{
	{".*", []string{".DS_Store", ".StackReplayTemp", "MyWorks/M 31/._a.fit", "a/.b/c/.d", "MyWorks/a.fit", "a.b", "MyWorks/x.y"}},
	{"*.tmp", []string{"x.mp4.tmp", "Video/x.mp4.TMP", "a.tmp", "tmp", "a.tmp/b", "a.tmpx"}},
	{"Thumbs.db", []string{"Thumbs.db", "MyWorks/M 31/thumbs.DB", "Thumbs.db.bak", "xThumbs.db"}},
	{"desktop.ini", []string{"desktop.ini", "a/Desktop.INI", "desktop.ini2"}},
	{"$RECYCLE.BIN", []string{"$Recycle.Bin", "RECYCLE.BIN", "$RECYCLE.BIN/x"}},
	{"System Volume Information", []string{"system volume information", "System  Volume Information", "SystemVolumeInformation"}},
	{"M?", []string{"M1", "a/M1", "m1", "M12", "M", "M1/x"}},
	{"Autorun?Dark", []string{"Autorun/Dark", "x/Autorun/Dark", "AutorunXDark"}},
	{"Autorun*", []string{"Autorun", "AutorunX", "Autorun/Dark"}},
	{"/log", []string{"log", "LOG", "Log", "MyWorks/M 31/log", "logs", "log/x", "a/log"}},
	{"/tmp_stitch", []string{"tmp_stitch", "tmp_stitch/a", "a/tmp_stitch"}},
	{"/batch_stack_tmp", []string{"batch_stack_tmp", "x/batch_stack_tmp"}},
	{"MyWorks/M 31", []string{"MyWorks/M 31", "myworks/m 31", "a/MyWorks/M 31", "M 31", "MyWorks/M 31/x", "MyWorks"}},
	{"/MyWorks/M 31", []string{"MyWorks/M 31", "a/MyWorks/M 31", "M 31", "MyWorks/M 3"}},
	{"MyWorks/*", []string{"MyWorks/M 31", "MyWorks/a.fit", "MyWorks", "MyWorks/M 31/a.fit", "x/MyWorks/a"}},
	{"*/Dark", []string{"Autorun/Dark", "Dark", "x/Autorun/Dark", "Autorun/Dark/x"}},
	{"/Autorun/?ark", []string{"Autorun/Dark", "autorun/dark", "Autorun/Dark/x", "Autorun/ark"}},
	{"Café.FIT", []string{"CAFÉ.fit", "Café.fit"}},
	{"ς", []string{"Σ", "ς"}},
	{"/MyWorks", []string{"MyWorks", "myworks/a.fit", "MyWorks/M 31/x/y.fit", "a.fit", "Autorun/MyWorks", "MyWorks2/a.fit", "x/MyWorks/a.fit", "Autorun"}},
	{"/Autorun/*/l.fit", []string{"Autorun/Light/l.fit", "Autorun/Dark/L.FIT/x", "Autorun/l.fit", "Autorun/Light/x/l.fit", "l.fit", "Autorun/Light", "Autorun/Bias", "MyWorks", "Autorun/Light/x", "Autorun/Light/l.fit2"}},
	{"*.jpg", []string{"a.jpg", "Preview/A.JPG", "x/y.jpg/z.fit", "a.jpeg", "Preview", "jpg/a.fit", "a/b/c"}},
	{"/Video", []string{"Video/y.mp4", "y.mp4", "Autorun/Video/y.mp4"}},
}

// Every pattern valid before .gitignore syntax keeps its meaning: for each
// old case, as a file and as a folder, the new matcher agrees with the old
// one on whether a path or a folder above it matches, and on whether
// something below a folder could. The one exception is ? or [...] against a
// name with non-ASCII letters: git matches a byte, not a character
// (TestByteWiseWildcards).
func TestLegacyPatternsKeepTheirMeaning(t *testing.T) {
	for _, tc := range legacyCases {
		// Every pattern was valid before.
		if _, err := legacyParse(tc.pattern); err != nil {
			t.Fatalf("%q: not a legacy pattern: %v", tc.pattern, err)
		}
		e, err := ParsePattern(tc.pattern)
		if err != nil {
			t.Errorf("%q: %v", tc.pattern, err)
			continue
		}
		old := legacy(tc.pattern)
		for _, p := range tc.paths {
			if strings.Contains(tc.pattern, "?") && strings.IndexFunc(p, func(r rune) bool { return r > 0x7f }) >= 0 {
				continue
			}
			for _, dir := range []bool{false, true} {
				if got, want := e.match(p, dir), old.matchWithin(p); got != want {
					t.Errorf("%q: match(%q, folder %t) = %t, legacy %t", tc.pattern, p, dir, got, want)
				}
			}
			if got, want := e.mayMatchBelow(p), old.mayMatchBelow(p); got != want {
				t.Errorf("%q: mayMatchBelow(%q) = %t, legacy %t", tc.pattern, p, got, want)
			}
		}
	}
	for _, d := range DefaultExcludes {
		if !slices.ContainsFunc(legacyCases, func(c struct {
			pattern string
			paths   []string
		}) bool {
			return c.pattern == d
		}) {
			t.Errorf("default exclude %q has no legacy case", d)
		}
	}
}

// legacyParse is the old pattern check, to show that legacyCases were all
// valid before.
func legacyParse(s string) (legacyPattern, error) {
	switch {
	case strings.ContainsAny(s, `[\`), strings.Contains(s, "**"), len(s) > 1 && strings.HasSuffix(s, "/"):
		return legacyPattern{}, errors.New("not supported")
	}
	for elem := range strings.SplitSeq(strings.TrimPrefix(s, "/"), "/") {
		if elem == "" || elem == "." || elem == ".." {
			return legacyPattern{}, errors.New("bad part")
		}
	}
	return legacy(s), nil
}

// ? and [...] match one byte, as in git, so they no longer match a
// non-ASCII letter of more than one byte. Before .gitignore syntax, ?
// matched one character.
func TestByteWiseWildcards(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		{"M?", "M星", false},
		{"M???", "M星", true},
		{"M*", "M星", true},
		{"M[a-z]", "Mé", false},
	} {
		e, err := ParsePattern(tc.pattern)
		if err != nil {
			t.Fatalf("%q: %v", tc.pattern, err)
		}
		if got := e.match(tc.path, false); got != tc.want {
			t.Errorf("%q: match(%q) = %t, want %t", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestExcludeMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		// files and dirs match, as a file or as a folder; otherFiles and
		// otherDirs do not.
		files, otherFiles, dirs, otherDirs []string
	}{
		// No slash: the last name, at any depth, file or folder, and
		// everything in a folder that matches.
		{pattern: ".*", files: []string{".DS_Store", "MyWorks/M 31/._a.fit", "a/.b/c"}, otherFiles: []string{"MyWorks/a.fit", "a.b"}},
		{pattern: "*.tmp", files: []string{"x.mp4.tmp", "Video/x.mp4.TMP", "a.tmp/b"}, otherFiles: []string{"tmp", "a.tmpx"}},
		{pattern: "M?", files: []string{"M1", "a/M1", "m1", "M1/x"}, otherFiles: []string{"M12", "M"}},
		// ? and * never cross a slash.
		{pattern: "Autorun?Dark", otherFiles: []string{"Autorun/Dark", "x/Autorun/Dark"}},
		{pattern: "Autorun*", files: []string{"Autorun", "AutorunX", "Autorun/Dark"}, otherFiles: []string{"autoru"}},
		// A leading slash anchors at the share root.
		{pattern: "/log", files: []string{"log", "LOG", "log/x"}, otherFiles: []string{"MyWorks/M 31/log", "logs", "a/log"}},
		// A slash in the middle anchors too, with or without the leading one.
		{pattern: "MyWorks/M 31", files: []string{"MyWorks/M 31", "myworks/m 31", "MyWorks/M 31/x"}, otherFiles: []string{"a/MyWorks/M 31", "M 31", "MyWorks"}},
		{pattern: "MyWorks/*", files: []string{"MyWorks/M 31", "MyWorks/M 31/a.fit"}, otherFiles: []string{"MyWorks", "x/MyWorks/a"}},
		// A trailing slash matches folders only, at any depth unless there
		// is another slash.
		{pattern: "log/", files: []string{"log/a.txt", "a/log/b"}, otherFiles: []string{"log", "a/log"}, dirs: []string{"log", "a/LOG"}},
		{pattern: "/MyWorks/M 31/", files: []string{"MyWorks/M 31/a.fit"}, otherFiles: []string{"MyWorks/M 31", "a/MyWorks/M 31/b"}, dirs: []string{"MyWorks/M 31"}, otherDirs: []string{"a/MyWorks/M 31"}},
		// ** stands for any number of folders, at the start, middle or end.
		{pattern: "**/Dark", files: []string{"Dark", "Autorun/Dark", "a/b/dark/x.fit"}, otherFiles: []string{"Darker"}},
		{pattern: "MyWorks/**/*.fit", files: []string{"MyWorks/a.fit", "MyWorks/M 31/x/a.FIT"}, otherFiles: []string{"a.fit", "x/MyWorks/a.fit", "MyWorks/a.jpg"}},
		{pattern: "/Autorun/**", files: []string{"Autorun/a", "Autorun/b/c"}, otherFiles: []string{"Autorun", "x/Autorun/a"}, otherDirs: []string{"Autorun"}},
		// Classes and escapes.
		{pattern: "M[0-9][0-9]", files: []string{"M31", "a/M42"}, otherFiles: []string{"M3", "MX1", "M311"}},
		{pattern: "M[!0-9]*", files: []string{"MyWorks"}, otherFiles: []string{"M31"}},
		{pattern: `\*.fit`, files: []string{"*.fit"}, otherFiles: []string{"a.fit"}},
		{pattern: `\!keep`, files: []string{"!keep"}, otherFiles: []string{"keep"}},
		{pattern: `\#notes`, files: []string{"#notes"}, otherFiles: []string{"notes"}},
		{pattern: `x\ `, files: []string{"x "}, otherFiles: []string{"x"}},
		// Spaces at the start and inside a name are kept.
		{pattern: " log", files: []string{" log"}, otherFiles: []string{"log"}},
		// Case is ignored by lowercasing both sides, with no Unicode
		// normalization or full case folding.
		{pattern: "Café.FIT", files: []string{"CAFÉ.fit"}, otherFiles: []string{"Café.fit"}},
		{pattern: "ς", otherFiles: []string{"Σ"}},
	} {
		e, err := ParsePattern(tc.pattern)
		if err != nil {
			t.Errorf("%q: %v", tc.pattern, err)
			continue
		}
		if e.flag != tc.pattern {
			t.Errorf("%q: flag %q, want it as given", tc.pattern, e.flag)
		}
		for _, c := range []struct {
			paths     []string
			dir, want bool
		}{{tc.files, false, true}, {tc.otherFiles, false, false}, {tc.dirs, true, true}, {tc.otherDirs, true, false}} {
			for _, p := range c.paths {
				if got := e.match(p, c.dir); got != c.want {
					t.Errorf("%q: match(%q, folder %t) = %t, want %t", tc.pattern, p, c.dir, got, c.want)
				}
			}
		}
	}
}

// The examples in git's gitignore documentation, "PATTERN FORMAT" and
// "EXAMPLES", hold here, where a path in a folder that matches matches too.
func TestGitDocExamples(t *testing.T) {
	type check struct {
		path      string
		dir, want bool
	}
	for _, tc := range []struct {
		pattern string
		checks  []check
	}{
		// "The pattern hello.* matches any file or directory whose name
		// begins with hello."
		{"hello.*", []check{{"hello.c", false, true}, {"a/hello.txt", true, true}, {"hellox", false, false}}},
		// "The pattern doc/frotz/ matches doc/frotz directory, but not
		// a/doc/frotz directory; however frotz/ matches frotz and a/frotz
		// that is a directory."
		{"doc/frotz/", []check{{"doc/frotz", true, true}, {"a/doc/frotz", true, false}, {"doc/frotz", false, false}}},
		{"frotz/", []check{{"frotz", true, true}, {"a/frotz", true, true}, {"a/frotz", false, false}}},
		// "foo/ will match a directory foo and paths underneath it, but
		// will not match a regular file or a symbolic link foo."
		{"foo/", []check{{"foo", true, true}, {"foo/x", false, true}, {"foo", false, false}}},
		// "The pattern foo/*, matches foo/test.json (a regular file),
		// foo/bar (a directory), but it does not match foo/bar/hello.c (a
		// regular file), as the asterisk in the pattern does not match
		// bar/hello.c" - here foo/bar/hello.c matches through foo/bar.
		{"foo/*", []check{{"foo/test.json", false, true}, {"foo/bar", true, true}, {"foo/bar/hello.c", false, true}, {"foo", true, false}}},
		// "/*.c" matches cat-file.c but not mozilla-sha1/sha1.c.
		{"/*.c", []check{{"cat-file.c", false, true}, {"mozilla-sha1/sha1.c", false, false}}},
		// "A leading **/ matches in all directories. For example, **/foo
		// matches file or directory foo anywhere, the same as pattern foo.
		// **/foo/bar matches file or directory bar anywhere that is
		// directly under directory foo."
		{"**/foo", []check{{"foo", false, true}, {"a/b/foo", true, true}, {"afoo", false, false}}},
		{"**/foo/bar", []check{{"foo/bar", false, true}, {"a/foo/bar", true, true}, {"foo/x/bar", false, false}}},
		// "A trailing /** matches everything inside. For example, abc/**
		// matches all files inside directory abc."
		{"abc/**", []check{{"abc/x", false, true}, {"abc/x/y", false, true}, {"abc", true, false}, {"x/abc/y", false, false}}},
		// "a/**/b matches a/b, a/x/b, a/x/y/b and so on."
		{"a/**/b", []check{{"a/b", false, true}, {"a/x/b", false, true}, {"a/x/y/b", false, true}, {"b", false, false}, {"x/a/b", false, false}}},
		// "Put a backslash (\) in front of the first hash for patterns that
		// begin with a hash." "Put a backslash (\) in front of the first !
		// for patterns that begin with a literal !"
		{`\#a`, []check{{"#a", false, true}}},
		{`\!important!.txt`, []check{{"!important!.txt", false, true}}},
		// "Trailing spaces are ignored unless they are quoted with
		// backslash."
		{`a\ `, []check{{"a ", false, true}, {"a", false, false}}},
		// "?" matches one character except /; "[a-zA-Z]" a range.
		{"a?c", []check{{"abc", false, true}, {"a/c", false, false}}},
		{"[a-c]x", []check{{"bx", false, true}, {"dx", false, false}}},
	} {
		e, err := ParsePattern(tc.pattern)
		if err != nil {
			t.Errorf("%q: %v", tc.pattern, err)
			continue
		}
		for _, c := range tc.checks {
			if got := e.match(c.path, c.dir); got != c.want {
				t.Errorf("%q: match(%q, folder %t) = %t, want %t", tc.pattern, c.path, c.dir, got, c.want)
			}
		}
	}
}

func TestExcludeParseErrors(t *testing.T) {
	for _, tc := range []struct {
		pattern, rule string
	}{
		{"", "with no empty, . or .. parts"},
		{"/", "with no empty, . or .. parts"},
		{"//", "with no empty, . or .. parts"},
		{".", "with no empty, . or .. parts"},
		{"..", "with no empty, . or .. parts"},
		{"./a", "with no empty, . or .. parts"},
		{"a/.", "with no empty, . or .. parts"},
		{"a/..", "with no empty, . or .. parts"},
		{"/../log", "with no empty, . or .. parts"},
		{"a//b", "with no empty, . or .. parts"},
		{"//x", "with no empty, . or .. parts"},
		{"x//", "with no empty, . or .. parts"},
		{"#notes", `write \# to match`},
		{"log ", "trailing spaces are ignored"},
		{"/log  ", "trailing spaces are ignored"},
		{`log\`, `a trailing \ has nothing to escape`},
		{`log\\\`, `a trailing \ has nothing to escape`},
		{"a\nb", "no line breaks"},
		{"a\r", "no line breaks"},
		{"[", "close each [ class"},
		{"M[42", "close each [ class"},
		{"[[:foo:]]", "close each [ class"},
		// The library splits on every /, so an escaped / is refused, and
		// a class holding a / is left unclosed.
		{`MyWorks\/M31`, "write a plain / between folders"},
		{`\/log`, "write a plain / between folders"},
		{`log\/`, "write a plain / between folders"},
		{`a\\\/b`, "write a plain / between folders"},
		{`[\/]`, "write a plain / between folders"},
		{"[a/]", "close each [ class"},
		{"/[a/]", "close each [ class"},
		{"MyWorks/[M/]31", "close each [ class"},
		{"a/[!/]", "close each [ class"},
	} {
		e, err := ParsePattern(tc.pattern)
		if err == nil {
			t.Errorf("%q: accepted as %+v", tc.pattern, e)
			continue
		}
		if !strings.Contains(err.Error(), tc.rule) {
			t.Errorf("%q: %q, want it to contain %q", tc.pattern, err, tc.rule)
		}
	}
	for _, p := range []string{"!x", "!", `!\!x`} {
		if _, err := ParsePattern(p); !errors.Is(err, ErrNegated) {
			t.Errorf("%q: %v, want ErrNegated", p, err)
		}
	}
	for _, p := range []string{
		"log/", "/log/", "M[42]", "**", "a**", "/**", "MyWorks/**/x", `MyWorks\x`, `\\`, `log\\`,
		`\!x`, `\#x`, `x\ `, " log", "M 31", "a#b", "a!b", `a\\/b`,
	} {
		if _, err := ParsePattern(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
	// In a\\/b the backslash is escaped, and the / separates names.
	if e, err := ParsePattern(`a\\/b`); err != nil || !e.match(`a\/b/c.fit`, false) || e.match("a/b", false) {
		t.Errorf(`a\\/b: %v; want it to match the folder b in the folder a\`, err)
	}
}

// An include keeps a file that it matches or that is in a folder it
// matches, and lists a folder only if it could match the folder or
// something below it: with an anchored pattern, the folder's names must
// match its leading names. With several includes any one will do.
func TestIncluded(t *testing.T) {
	for _, tc := range []struct {
		includes   []string
		files      []string // kept
		otherFiles []string // left out
		dirs       []string // listed
		otherDirs  []string // left out, never read
	}{
		{
			includes:   []string{"/MyWorks"},
			files:      []string{"MyWorks", "myworks/a.fit", "MyWorks/M 31/x/y.fit"},
			otherFiles: []string{"a.fit", "Autorun/MyWorks", "MyWorks2/a.fit", "x/MyWorks/a.fit"},
			dirs:       []string{"MyWorks", "MYWORKS", "MyWorks/M 31", "MyWorks/M 31/x"},
			otherDirs:  []string{"Autorun", "MyWorks2", "x/MyWorks", "Autorun/MyWorks"},
		},
		{
			includes:   []string{"/MyWorks/M 31"},
			files:      []string{"MyWorks/M 31", "MyWorks/M 31/a.fit"},
			otherFiles: []string{"MyWorks", "MyWorks/a.fit", "MyWorks/M 3/a.fit", "M 31/a.fit"},
			dirs:       []string{"MyWorks", "MyWorks/M 31", "MyWorks/m 31/sub"},
			otherDirs:  []string{"Autorun", "MyWorks/M 42", "MyWorks/M 3", "M 31"},
		},
		{
			includes:   []string{"/Autorun/*/l.fit"},
			files:      []string{"Autorun/Light/l.fit", "Autorun/Dark/L.FIT/x"},
			otherFiles: []string{"Autorun/l.fit", "Autorun/Light/x/l.fit", "l.fit", "Autorun/Light"},
			dirs:       []string{"Autorun", "Autorun/Light", "Autorun/Bias", "Autorun/Light/l.fit"},
			otherDirs:  []string{"MyWorks", "Autorun/Light/x", "Autorun/Light/l.fit2"},
		},
		{
			includes:   []string{"*.jpg"},
			files:      []string{"a.jpg", "Preview/A.JPG", "x/y.jpg/z.fit"},
			otherFiles: []string{"a.jpeg", "Preview", "jpg/a.fit"},
			dirs:       []string{"Preview", "a/b/c"},
		},
		// ** prunes only from where it stands: the names before it must
		// still match.
		{
			includes:   []string{"/MyWorks/**/*.fit"},
			files:      []string{"MyWorks/a.fit", "MyWorks/M 31/x/b.FIT", "MyWorks/c.fit/d.txt"},
			otherFiles: []string{"a.fit", "Autorun/a.fit", "MyWorks/a.jpg", "x/MyWorks/a.fit"},
			dirs:       []string{"MyWorks", "MyWorks/M 31", "MyWorks/M 31/x/y"},
			otherDirs:  []string{"Autorun", "MyWorks2", "x/MyWorks", "Autorun/MyWorks"},
		},
		{
			includes:   []string{"/Autorun/*/**/l.fit"},
			files:      []string{"Autorun/Light/l.fit", "Autorun/Light/x/y/l.fit"},
			otherFiles: []string{"Autorun/l.fit", "MyWorks/Light/l.fit"},
			dirs:       []string{"Autorun", "Autorun/Light", "Autorun/Light/x/y"},
			otherDirs:  []string{"MyWorks", "MyWorks/Light"},
		},
		{
			includes:   []string{"**/Light"},
			files:      []string{"Light", "Autorun/Light/l.fit", "a/b/light/c/d.fit"},
			otherFiles: []string{"Autorun/Dark/d.fit", "Lights/a.fit"},
			dirs:       []string{"Autorun", "a/b/c"},
		},
		// A trailing / selects folders only, and everything in them.
		{
			includes:   []string{"/Preview/"},
			files:      []string{"Preview/a.jpg", "Preview/x/b.jpg"},
			otherFiles: []string{"Preview", "x/Preview/a.jpg"},
			dirs:       []string{"Preview", "Preview/x"},
			otherDirs:  []string{"x", "x/Preview"},
		},
		{
			includes:   []string{"Light/"},
			files:      []string{"Light/a.fit", "Autorun/Light/l.fit"},
			otherFiles: []string{"Light", "Autorun/Light"},
			dirs:       []string{"Autorun", "Autorun/Light"},
		},
		{
			includes:   []string{"*.jpg", "/Video"},
			files:      []string{"a.jpg", "Video/y.mp4"},
			otherFiles: []string{"y.mp4", "Autorun/Video/y.mp4"},
			dirs:       []string{"Autorun", "Video"},
		},
	} {
		s := selector(t, nil, tc.includes)
		for _, c := range []struct {
			paths []string
			dir   bool
			want  bool
		}{{tc.files, false, true}, {tc.otherFiles, false, false}, {tc.dirs, true, true}, {tc.otherDirs, true, false}} {
			for _, p := range c.paths {
				if got := s.included(p, c.dir); got != c.want {
					t.Errorf("%q: included(%q, folder %t) = %t, want %t", tc.includes, p, c.dir, got, c.want)
				}
			}
		}
	}
	// No include keeps everything.
	if s := (Selector{}); !s.included("a", false) || !s.included("a", true) {
		t.Errorf("without includes: %+v; want everything kept", s)
	}
}

// A space before a / is part of the name, also where the walk decides
// which folders could hold a match: a walk with the include lists the
// selected file, and leaves out the folder without the space unread.
func TestIncludeSpaceBeforeSlash(t *testing.T) {
	file := &fstest.MapFile{Data: []byte("x")}
	share := remote.NewFake(fstest.MapFS{
		"folder /file.fit":   file,
		"folder/file.fit":    file,
		"a/folder /file.fit": file,
		"a/folder/file.fit":  file,
	})
	for _, tc := range []struct {
		include string
		want    []string
	}{
		{"/folder /file.fit", []string{"folder /file.fit"}},
		{"folder /file.fit", []string{"folder /file.fit"}},
		{"/folder /**/file.fit", []string{"folder /file.fit"}},
		{"/a/folder /file.fit", []string{"a/folder /file.fit"}},
		{"/folder /", []string{"folder /file.fit"}},
	} {
		s := selector(t, nil, []string{tc.include})
		kept, _, err := s.Walk(t.Context(), share, nil)
		if err != nil {
			t.Fatal(err)
		}
		var files, dirs []string
		for _, e := range kept {
			if e.Kind == remote.Dir {
				dirs = append(dirs, e.Path)
			} else {
				files = append(files, e.Path)
			}
		}
		if !slices.Equal(files, tc.want) {
			t.Errorf("%q: walk kept files %q, want %q", tc.include, files, tc.want)
		}
		if slices.Contains(dirs, "folder") || slices.Contains(dirs, "a/folder") {
			t.Errorf("%q: walk listed a folder without the space: %q", tc.include, dirs)
		}
		for _, p := range tc.want {
			if s.LeavesOut(p, false) {
				t.Errorf("%q: LeavesOut(%q) = true", tc.include, p)
			}
		}
	}
}

// TestWalkKeptParents checks that the walk records the parent of every
// entry it left out, whether an exclude or an include left it out, so that
// delete keeps a folder with something unlisted in it.
func TestWalkKeptParents(t *testing.T) {
	file := &fstest.MapFile{Data: []byte("x")}
	share := fstest.MapFS{
		"top.jpg":             file,
		".DS_Store":           file,
		"MyWorks/a/x.jpg":     file,
		"MyWorks/a/x.fit":     file,
		"MyWorks/b/y.jpg":     file,
		"MyWorks/c/.hidden":   file,
		"MyWorks/d/e/z.fit":   file,
		"MyWorks/only/f.jpg":  file,
		"MyWorks/skip/g.tmp":  file,
		"MyWorks/skip/in.jpg": file,
	}
	s := selector(t, append(slices.Clone(DefaultExcludes), "/MyWorks/skip"), []string{"*.jpg"})
	_, out, err := s.Walk(t.Context(), remote.NewFake(share), nil)
	if err != nil {
		t.Fatal(err)
	}
	// .DS_Store is excluded at the root, x.fit and z.fit miss the include,
	// .hidden is excluded, and /MyWorks/skip is excluded as a whole.
	want := map[string]bool{".": true, "MyWorks": true, "MyWorks/a": true, "MyWorks/c": true, "MyWorks/d/e": true}
	if !maps.Equal(out.keptParents, want) {
		t.Errorf("keptParents = %v, want %v", out.keptParents, want)
	}
}

// TestLeavesOut checks that LeavesOut leaves out exactly what a walk with
// the same selection does not list: by name, by path, inside a folder left
// out, and outside the includes.
func TestLeavesOut(t *testing.T) {
	file := &fstest.MapFile{Data: []byte("x")}
	share := remote.NewFake(fstest.MapFS{
		"top.jpg":              file,
		".DS_Store":            file,
		"log/a.txt":            file,
		"MyWorks/a/x.jpg":      file,
		"MyWorks/a/x.fit":      file,
		"MyWorks/a/._x.jpg":    file,
		"MyWorks/skip/in.jpg":  file,
		"MyWorks/skip/d/z.jpg": file,
		"MyWorks/log/b.txt":    file,
		"Video/v.mp4":          file,
	})
	all, _, err := Selector{}.Walk(t.Context(), share, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		excludes, includes []string
		left               []string
	}{
		{nil, nil, nil},
		{DefaultExcludes, nil, []string{".DS_Store", "log", "log/a.txt", "MyWorks/a/._x.jpg"}},
		{[]string{"/MyWorks/skip"}, nil, []string{"MyWorks/skip", "MyWorks/skip/in.jpg", "MyWorks/skip/d", "MyWorks/skip/d/z.jpg"}},
		{[]string{"*.FIT", "skip"}, []string{"/MyWorks"}, []string{
			"top.jpg", ".DS_Store", "log", "log/a.txt", "MyWorks/a/x.fit", "MyWorks/skip", "MyWorks/skip/in.jpg",
			"MyWorks/skip/d", "MyWorks/skip/d/z.jpg", "Video", "Video/v.mp4",
		}},
		{nil, []string{"*.jpg"}, []string{".DS_Store", "log/a.txt", "MyWorks/a/x.fit", "MyWorks/log/b.txt", "Video/v.mp4"}},
	} {
		s := selector(t, tc.excludes, tc.includes)
		kept, _, err := s.Walk(t.Context(), share, nil)
		if err != nil {
			t.Fatal(err)
		}
		listed := map[string]bool{}
		for _, e := range kept {
			listed[e.Path] = true
		}
		var left []string
		for _, e := range all {
			got := s.LeavesOut(e.Path, e.Kind == remote.Dir)
			if got == listed[e.Path] {
				t.Errorf("%q %q: LeavesOut(%q) = %t, but the walk listed it: %t", tc.excludes, tc.includes, e.Path, got, listed[e.Path])
			}
			if got {
				left = append(left, e.Path)
			}
		}
		slices.Sort(left)
		want := slices.Sorted(slices.Values(tc.left))
		if !slices.Equal(left, want) {
			t.Errorf("%q %q: left out %q, want %q", tc.excludes, tc.includes, left, want)
		}
	}
}

// A pattern of many parts keeps at most maxPrefixes prefixes, so parsing
// it costs memory in proportion to its length.
func TestPrefixBudget(t *testing.T) {
	p, err := ParsePattern("/" + strings.Repeat("a/", 1023) + "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.prefixes) != maxPrefixes {
		t.Errorf("%d prefixes, want %d", len(p.prefixes), maxPrefixes)
	}
}

// Past maxPrefixes names a walk lists every folder, and still keeps only
// the files the whole pattern selects; a folder that differs within the
// budget is still left out unread.
func TestIncludeBeyondPrefixBudget(t *testing.T) {
	deep := strings.Repeat("d/", maxPrefixes+3)
	file := &fstest.MapFile{Data: []byte("x")}
	share := remote.NewFake(fstest.MapFS{
		deep + "a.fit": file,
		deep + "b.fit": file,
		strings.Repeat("d/", maxPrefixes+1) + "x/a.fit": file,
		"d/e/" + deep + "a.fit":                         file,
	})
	s := selector(t, nil, []string{"/" + deep + "a.fit"})
	kept, _, err := s.Walk(t.Context(), share, nil)
	if err != nil {
		t.Fatal(err)
	}
	var files, dirs []string
	for _, e := range kept {
		if e.Kind == remote.Dir {
			dirs = append(dirs, e.Path)
		} else {
			files = append(files, e.Path)
		}
	}
	if want := []string{deep + "a.fit"}; !slices.Equal(files, want) {
		t.Errorf("walk kept files %q, want %q", files, want)
	}
	if !slices.Contains(dirs, strings.Repeat("d/", maxPrefixes+1)+"x") {
		t.Errorf("a folder past the budget was not listed: %q", dirs)
	}
	if slices.Contains(dirs, "d/e") {
		t.Errorf("a folder that differs within the budget was listed: %q", dirs)
	}
}
