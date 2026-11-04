package plan

import (
	"io/fs"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOldEnough(t *testing.T) {
	now := time.Date(2026, 11, 9, 9, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour
	for _, tc := range []struct {
		mtime time.Time
		limit time.Duration
		want  bool
	}{
		{now.Add(-week), week, true},
		{now.Add(-week + time.Nanosecond), week, false},
		{now.Add(-week - time.Nanosecond), week, true},
		{now.Add(time.Hour), week, false},
		{now.Add(time.Hour), 0, true},
		{now, 0, true},
		{now, time.Nanosecond, false},
	} {
		if got := OldEnough(now, tc.mtime, tc.limit); got != tc.want {
			t.Errorf("OldEnough(now, %v, %v) = %v, want %v", tc.mtime, tc.limit, got, tc.want)
		}
	}
}

// shareValid is remote.ValidPath's rule, which plan does not import.
func shareValid(p string) bool {
	return p != "." && fs.ValidPath(p) && !strings.Contains(p, `\`)
}

func TestDecideDelete(t *testing.T) {
	const week = 7 * 24 * time.Hour
	file := Entry{Path: testPath, Size: testSize, ModTime: testMtime, Regular: true}
	recorded := map[string]Recorded{testPath: {Size: testSize, ModTime: testMtime}}
	// old is a now at which file is exactly a week old.
	old := testMtime.Add(week)
	clash := `name clash: "MyWorks/M 31" and "MyWorks/m 31" differ only in case or Unicode normalization`
	other := func(f func(*Entry)) Entry {
		e := file
		f(&e)
		return e
	}
	for _, tc := range []struct {
		name    string
		listing []Entry
		state   map[string]Recorded
		now     time.Time
		limit   time.Duration
		want    DeleteDecision
	}{
		{"recorded and unchanged", []Entry{file}, recorded, old, week, DeleteDecision{Kind: Delete}},
		{"no state read", []Entry{file}, nil, old, week, DeleteDecision{Kind: Delete}},
		{"same instant elsewhere", []Entry{other(func(e *Entry) { e.ModTime = testMtime.In(elsewhere) })}, recorded, old, week, DeleteDecision{Kind: Delete}},
		{"mtime 100 ns later", []Entry{other(func(e *Entry) { e.ModTime = testMtime.Add(100) })}, recorded, old.Add(time.Hour), week, DeleteDecision{Kind: Keep, Reason: KeepChanged, Warn: true}},
		{"size one more", []Entry{other(func(e *Entry) { e.Size++ })}, recorded, old, week, DeleteDecision{Kind: Keep, Reason: KeepChanged, Warn: true}},
		{"size one less", []Entry{other(func(e *Entry) { e.Size-- })}, recorded, old, week, DeleteDecision{Kind: Keep, Reason: KeepChanged, Warn: true}},
		{"not recorded", []Entry{file}, map[string]Recorded{}, old, week, DeleteDecision{Kind: Keep, Reason: KeepNotState}},
		{"recorded under another case", []Entry{file}, map[string]Recorded{strings.Replace(testPath, "M 31", "m 31", 1): {Size: testSize, ModTime: testMtime}}, old, week, DeleteDecision{Kind: Keep, Reason: clash, Warn: true}},
		{"listed under another case too", []Entry{file, other(func(e *Entry) { e.Path = strings.Replace(testPath, "M 31", "m 31", 1) })}, nil, old, week, DeleteDecision{Kind: Keep, Reason: clash, Warn: true}},
		{"exactly the limit", []Entry{file}, recorded, old, week, DeleteDecision{Kind: Delete}},
		{"1 ns short", []Entry{file}, recorded, old.Add(-1), week, DeleteDecision{Kind: Recent}},
		{"future mtime", []Entry{file}, recorded, testMtime.Add(-time.Hour), week, DeleteDecision{Kind: Recent}},
		{"future mtime, no limit", []Entry{file}, recorded, testMtime.Add(-time.Hour), 0, DeleteDecision{Kind: Delete}},
		{"recent and not recorded", []Entry{file}, map[string]Recorded{}, old.Add(-1), week, DeleteDecision{Kind: Recent}},
		{"recent clash", []Entry{file, other(func(e *Entry) { e.Path = strings.Replace(testPath, "M 31", "m 31", 1) })}, nil, old.Add(-1), week, DeleteDecision{Kind: Keep, Reason: clash, Warn: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideDelete(DeleteInput{Listing: tc.listing, State: tc.state, Now: tc.now, OlderThan: tc.limit, ValidPath: shareValid})
			if d := got[tc.listing[0].Path]; d != tc.want {
				t.Errorf("decision = %+v, want %+v", d, tc.want)
			}
		})
	}

	t.Run("invalid paths", func(t *testing.T) {
		for _, p := range []string{`MyWorks/sub\saved.fit`, "/MyWorks/a.fit", "MyWorks//a.fit", "MyWorks/./a.fit", "MyWorks/../a.fit"} {
			e := Entry{Path: p, Size: 1, ModTime: testMtime, Regular: true}
			// The path is checked first: before the age, and before the
			// record, which matches.
			for _, state := range []map[string]Recorded{nil, {p: {Size: 1, ModTime: testMtime}}} {
				got := DecideDelete(DeleteInput{Listing: []Entry{e}, State: state, Now: testMtime, OlderThan: week, ValidPath: shareValid})
				if want := (DeleteDecision{Kind: Keep, Reason: KeepPath, Warn: true}); got[p] != want {
					t.Errorf("%q: decision = %+v, want %+v", p, got[p], want)
				}
			}
		}
	})

	t.Run("only regular files", func(t *testing.T) {
		got := DecideDelete(DeleteInput{Listing: []Entry{{Path: "MyWorks"}, file, {Path: "MyWorks/link"}}, State: nil, Now: old, OlderThan: week, ValidPath: shareValid})
		if keys := slices.Collect(maps.Keys(got)); !slices.Equal(keys, []string{testPath}) {
			t.Errorf("decisions on %q, want only %q", keys, testPath)
		}
	})
}

// folderEntry is a listed entry for TestFolders: a folder, a removed file,
// or anything else, which stays.
type folderEntry struct {
	path string
	kind byte // 'd' folder, 'x' a file the run removes, '-' anything that stays
}

// emptied runs Folders the way delete does, in reverse listing order, and
// returns the folders it would remove.
func emptied(listing []folderEntry, leftOut map[string]bool) []string {
	var entries []Entry
	for _, e := range listing {
		entries = append(entries, Entry{Path: e.path, Regular: e.kind != 'd'})
	}
	f := NewFolders(entries, leftOut)
	var removed []string
	for _, e := range slices.Backward(listing) {
		switch {
		case e.kind == 'd' && f.Emptied(e.path):
			removed = append(removed, e.path)
		case e.kind != 'x':
			f.Kept(e.path)
		}
	}
	return removed
}

func TestFolders(t *testing.T) {
	for _, tc := range []struct {
		name    string
		listing []folderEntry
		leftOut map[string]bool
		want    []string
	}{
		{"emptied", []folderEntry{{"a", 'd'}, {"a/x.fit", 'x'}, {"a/y.jpg", 'x'}}, nil, []string{"a"}},
		{"an excluded child", []folderEntry{{"a", 'd'}, {"a/x.fit", 'x'}}, map[string]bool{"a": true}, nil},
		{"only pruned children", []folderEntry{{"a", 'd'}, {"b", 'd'}, {"b/x.fit", 'x'}}, map[string]bool{"a": true}, []string{"b"}},
		{"a child that stays", []folderEntry{{"a", 'd'}, {"a/link", '-'}, {"a/x.fit", 'x'}}, nil, nil},
		{"nested, emptied together", []folderEntry{{"a", 'd'}, {"a/b", 'd'}, {"a/b/c", 'd'}, {"a/b/c/x.fit", 'x'}, {"a/y.fit", 'x'}}, nil, []string{"a/b/c", "a/b", "a"}},
		{"a subfolder that stays", []folderEntry{{"a", 'd'}, {"a/b", 'd'}, {"a/b/x.fit", '-'}, {"a/y.fit", 'x'}}, nil, nil},
		{"already empty", []folderEntry{{"a", 'd'}, {"a/e", 'd'}, {"a/x.fit", 'x'}}, nil, nil},
		{"empty at the top", []folderEntry{{"e", 'd'}}, nil, nil},
		{"root", []folderEntry{{"x.fit", 'x'}}, nil, nil},
		{"root with something left out", []folderEntry{{"x.fit", 'x'}}, map[string]bool{".": true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := emptied(tc.listing, tc.leftOut); !slices.Equal(got, tc.want) {
				t.Errorf("removed %q, want %q", got, tc.want)
			}
		})
	}
	if (&Folders{had: map[string]bool{".": true}, kept: map[string]bool{}}).Emptied(".") {
		t.Error("the root is emptied")
	}
}
