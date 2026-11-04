package engine

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zmerlynn/seestar-copy/internal/remote"
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

func TestExcludeMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		match   []string
		miss    []string
	}{
		// No slash: the last name, at any depth, file or folder.
		{".*", []string{".DS_Store", ".StackReplayTemp", "MyWorks/M 31/._a.fit", "a/.b/c/.d"}, []string{"MyWorks/a.fit", "a.b", "MyWorks/x.y"}},
		{"*.tmp", []string{"x.mp4.tmp", "Video/x.mp4.TMP", "a.tmp"}, []string{"tmp", "a.tmp/b", "a.tmpx"}},
		{"Thumbs.db", []string{"Thumbs.db", "MyWorks/M 31/thumbs.DB"}, []string{"Thumbs.db.bak", "xThumbs.db"}},
		{"$RECYCLE.BIN", []string{"$Recycle.Bin"}, []string{"RECYCLE.BIN"}},
		{"System Volume Information", []string{"system volume information"}, []string{"System  Volume Information", "SystemVolumeInformation"}},
		{"M?", []string{"M1", "a/M1", "m1", "M星"}, []string{"M12", "M", "M1/x"}},
		// ? and * never cross a slash.
		{"Autorun?Dark", nil, []string{"Autorun/Dark", "x/Autorun/Dark"}},
		{"Autorun*", []string{"Autorun", "AutorunX"}, []string{"Autorun/Dark"}},
		// A leading slash anchors at the share root.
		{"/log", []string{"log", "LOG", "Log"}, []string{"MyWorks/M 31/log", "logs", "log/x", "a/log"}},
		// A slash in the middle anchors too, with or without the leading one.
		{"MyWorks/M 31", []string{"MyWorks/M 31", "myworks/m 31"}, []string{"a/MyWorks/M 31", "M 31", "MyWorks/M 31/x", "MyWorks"}},
		{"/MyWorks/M 31", []string{"MyWorks/M 31"}, []string{"a/MyWorks/M 31", "M 31"}},
		{"MyWorks/*", []string{"MyWorks/M 31", "MyWorks/a.fit"}, []string{"MyWorks", "MyWorks/M 31/a.fit", "x/MyWorks/a"}},
		{"*/Dark", []string{"Autorun/Dark"}, []string{"Dark", "x/Autorun/Dark"}},
		{"/Autorun/?ark", []string{"Autorun/Dark", "autorun/dark"}, []string{"Autorun/Dark/x", "Autorun/ark"}},
		// Case is ignored by lowercasing both sides, with no Unicode
		// normalization or full case folding.
		{"Café.FIT", []string{"CAFÉ.fit"}, []string{"Cafe\u0301.fit"}},
		{"ς", nil, []string{"Σ"}},
	} {
		e, err := ParsePattern(tc.pattern)
		if err != nil {
			t.Errorf("%q: %v", tc.pattern, err)
			continue
		}
		if e.flag != tc.pattern {
			t.Errorf("%q: flag %q, want it as given", tc.pattern, e.flag)
		}
		for _, p := range tc.match {
			if !e.match(p) {
				t.Errorf("%q does not match %q", tc.pattern, p)
			}
		}
		for _, p := range tc.miss {
			if e.match(p) {
				t.Errorf("%q matches %q", tc.pattern, p)
			}
		}
	}
}

func TestExcludeParseErrors(t *testing.T) {
	for _, p := range []string{
		"", "/", ".", "..", "./a", "a/.", "a/..", "/../log", "a//b", "//x",
		"log/", "/log/", "//",
		"M[42]", "[", `MyWorks\x`, `\`,
		"**", "a**", "MyWorks/**/x",
	} {
		if e, err := ParsePattern(p); err == nil {
			t.Errorf("%q: accepted as %+v", p, e)
		}
	}
	// Spaces are kept, not trimmed.
	for _, p := range []string{" log", "log ", "/log "} {
		e, err := ParsePattern(p)
		if err != nil || e.match("log") || !e.match(strings.TrimPrefix(p, "/")) {
			t.Errorf("%q: %+v, %v; want spaces kept", p, e, err)
		}
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
	_, out, err := s.Walk(t.Context(), remote.NewFake(share))
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
	all, _, err := Selector{}.Walk(t.Context(), share)
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
		kept, _, err := s.Walk(t.Context(), share)
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
