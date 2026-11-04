package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"testing"
	"time"
)

const (
	testPath = "MyWorks/M 31/Light_M 31_10.0s_IRCUT_20261029-033119.fit"
	testSize = 16594560
)

var (
	// testMtime has sub-second precision, as SMB times do.
	testMtime = time.Date(2026, 10, 29, 3, 31, 19, 123456789, time.UTC)
	// elsewhere is another location, so the same instant is a different
	// time.Time value: only Equal treats them as the same.
	elsewhere = time.FixedZone("UTC+9", 9*60*60)
)

// stableInput lists one regular file an hour old, unchanged since the
// previous listing, not in the state and with no local stat yet.
func stableInput() Input {
	e := Entry{Path: testPath, Size: testSize, ModTime: testMtime, Regular: true}
	return Input{
		Listing:      []Entry{e},
		Previous:     []Entry{e},
		State:        map[string]Recorded{},
		Local:        map[string]Local{},
		Now:          testMtime.Add(time.Hour),
		ReadyAge:     2 * time.Minute,
		FirstPassAge: 10 * time.Minute,
	}
}

func absent(in *Input) { in.Local[testPath] = Local{} }

func recorded(size int64, mtime time.Time) func(*Input) {
	return func(in *Input) { in.State[testPath] = Recorded{Size: size, ModTime: mtime} }
}

func local(l Local) func(*Input) {
	return func(in *Input) { in.Local[testPath] = l }
}

func listedAs(path string) func(*Input) {
	return func(in *Input) {
		in.Listing[0].Path = path
		in.Previous = []Entry{in.Listing[0]}
		in.Local[path] = Local{}
	}
}

// listedAt lists the file, unchanged since the previous listing and absent
// locally, with mtime t.
func listedAt(t time.Time) func(*Input) {
	return func(in *Input) {
		in.Listing[0].ModTime = t
		in.Previous = []Entry{in.Listing[0]}
		in.Local[testPath] = Local{}
	}
}

// localAt is a local file of the listed size with mtime t.
func localAt(t time.Time) Local {
	return Local{Exists: true, Regular: true, Size: testSize, ModTime: t}
}

// TestDecideOneFile runs each rule on a single listed file. Every case also
// checks NeedLocal, which must ask for the stat exactly when Decide uses it.
func TestDecideOneFile(t *testing.T) {
	tests := []struct {
		name     string
		setup    []func(*Input)
		wantNeed bool
		want     Kind
		wantOver bool
		reason   string
	}{
		// Readiness.
		{name: "stable and new", setup: fns(absent), wantNeed: true, want: Copy},
		{name: "exactly ready-age old", setup: fns(absent, now(2*time.Minute)), wantNeed: true, want: Copy},
		{name: "a nanosecond short of ready-age", setup: fns(absent, now(2*time.Minute-1)), want: Wait},
		{name: "future mtime", setup: fns(absent, now(-time.Second)), want: Wait},
		{name: "future mtime with zero ready-age", setup: fns(absent, readyAge(0), now(-1)), want: Wait},
		{name: "zero age with zero ready-age", setup: fns(absent, readyAge(0), now(0)), wantNeed: true, want: Copy},
		{name: "new since the previous listing", setup: fns(absent, prev(Entry{Path: "MyWorks/other.fit", Size: testSize, ModTime: testMtime, Regular: true})), want: Wait},
		{name: "previous listing was empty", setup: fns(absent, prev()), want: Wait},
		{name: "previous size differs", setup: fns(absent, prevChanged(testSize-1, testMtime)), want: Wait},
		{name: "previous mtime differs by a nanosecond", setup: fns(absent, prevChanged(testSize, testMtime.Add(1))), want: Wait},
		{name: "previous mtime in another location", setup: fns(absent, prevChanged(testSize, testMtime.In(elsewhere))), wantNeed: true, want: Copy},
		{name: "previously a directory", setup: fns(absent, prev(Entry{Path: testPath, ModTime: testMtime})), want: Wait},
		{name: "previously listed twice", setup: fns(absent, func(in *Input) { in.Previous = append(in.Previous, in.Previous[0]) }), want: Wait},
		{name: "first pass, exactly first-pass-age old", setup: fns(absent, firstPass, now(10*time.Minute)), wantNeed: true, want: Copy},
		{name: "first pass, a nanosecond short", setup: fns(absent, firstPass, now(10*time.Minute-1)), want: Wait},
		{name: "first pass ignores Previous", setup: fns(absent, firstPass, prevChanged(1, testMtime.Add(time.Second))), wantNeed: true, want: Copy},
		{name: "first pass with no previous listing", setup: fns(absent, firstPass, prev()), wantNeed: true, want: Copy},
		{name: "first pass still needs ready-age", setup: fns(absent, firstPass, firstPassAge(time.Minute), now(2*time.Minute-1)), want: Wait},
		{name: "first pass, past both ages", setup: fns(absent, firstPass, firstPassAge(time.Minute), now(2*time.Minute)), wantNeed: true, want: Copy},

		// State.
		{name: "in state", setup: fns(recorded(testSize, testMtime)), want: Done},
		{name: "in state, mtime in another location", setup: fns(recorded(testSize, testMtime.In(elsewhere))), want: Done},
		{name: "in state ignores a differing local file", setup: fns(recorded(testSize, testMtime), local(Local{Exists: true, Regular: true, Size: 1})), want: Done},
		{name: "in state ignores a local stat error", setup: fns(recorded(testSize, testMtime), local(Local{Err: fs.ErrPermission})), want: Done},
		{name: "in state and not ready", setup: fns(recorded(testSize, testMtime), now(0), prev()), want: Done},
		{name: "in state, first pass, young", setup: fns(recorded(testSize, testMtime), firstPass, now(0)), want: Done},
		{name: "in state, size changed", setup: fns(recorded(testSize+1, testMtime)), want: Replace},
		{name: "in state, mtime a nanosecond off", setup: fns(recorded(testSize, testMtime.Add(-1))), want: Replace},
		{name: "in state, changed and too new", setup: fns(recorded(testSize+1, testMtime), now(time.Minute)), want: Wait},
		{name: "in state, changed and new since the previous listing", setup: fns(recorded(testSize+1, testMtime), prevChanged(testSize+2, testMtime)), want: Wait},
		{name: "in state, changed, first pass, too new", setup: fns(recorded(testSize+1, testMtime), firstPass, now(5*time.Minute)), want: Wait},

		// The local file.
		{name: "identical local file", setup: fns(local(localAt(testMtime))), wantNeed: true, want: Adopt},
		{name: "identical local file, mtime in another location", setup: fns(local(Local{Exists: true, Regular: true, Size: testSize, ModTime: testMtime.In(elsewhere)})), wantNeed: true, want: Adopt},
		{name: "local file of another size", setup: fns(local(Local{Exists: true, Regular: true, Size: testSize - 1, ModTime: testMtime})), wantNeed: true, want: Copy, wantOver: true},
		{name: "local mtime a nanosecond off", setup: fns(local(localAt(testMtime.Add(1)))), wantNeed: true, want: Adopt},
		{name: "local mtime 10 ms behind", setup: fns(local(localAt(testMtime.Add(-10 * time.Millisecond)))), wantNeed: true, want: Adopt},
		{name: "local mtime a second ahead", setup: fns(local(localAt(testMtime.Add(time.Second)))), wantNeed: true, want: Adopt},
		{name: "local mtime 2 s ahead", setup: fns(local(localAt(testMtime.Add(2 * time.Second)))), wantNeed: true, want: Adopt},
		{name: "local mtime 2 s behind", setup: fns(local(localAt(testMtime.Add(-2 * time.Second)))), wantNeed: true, want: Adopt},
		{name: "local mtime 2 s and a nanosecond ahead", setup: fns(local(localAt(testMtime.Add(2*time.Second + 1)))), wantNeed: true, want: Copy, wantOver: true},
		{name: "local mtime 3 s behind", setup: fns(local(localAt(testMtime.Add(-3 * time.Second)))), wantNeed: true, want: Copy, wantOver: true},
		{name: "local directory", setup: fns(local(Local{Exists: true, Size: testSize, ModTime: testMtime})), wantNeed: true, want: Fail, reason: "local path exists and is not a regular file"},
		{name: "local stat failed", setup: fns(local(Local{Err: fs.ErrPermission})), wantNeed: true, want: Fail, reason: "local stat failed: permission denied"},
		{name: "local stat failed beside a match", setup: fns(local(Local{Exists: true, Regular: true, Size: testSize, ModTime: testMtime, Err: errors.New("i/o error")})), wantNeed: true, want: Fail, reason: "local stat failed: i/o error"},
		{name: "no local stat supplied", wantNeed: true, want: Fail, reason: "no local stat for this path"},

		// Paths and sizes.
		{name: "escaping path", setup: fns(listedAs("MyWorks/../../etc/passwd")), want: Fail, reason: `path element ".."`},
		{name: "escaping path that is also in the state", setup: fns(listedAs("/etc/passwd"), func(in *Input) { in.State["/etc/passwd"] = Recorded{Size: testSize, ModTime: testMtime} }), want: Fail, reason: "absolute path"},
		{name: "escaping path that is not ready", setup: fns(listedAs(`MyWorks\..\x`), now(0)), want: Fail, reason: "backslash in path"},
		{name: "temporary file name", setup: fns(listedAs("MyWorks/.seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tmp")), want: Fail, reason: `name ".seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tmp" is reserved for temporary files`},
		{name: "old temporary folder", setup: fns(listedAs(".seestar-copy-tmp/0123")), wantNeed: true, want: Copy},
		{name: "colon elsewhere", setup: fns(listedAs("MyWorks/a:b.fit")), wantNeed: true, want: Copy},
		{name: "colon on Windows", setup: fns(listedAs("MyWorks/a:b.fit"), windows), want: Fail, reason: `character ':' is not allowed on Windows`},
		{name: "reserved name elsewhere", setup: fns(listedAs("MyWorks/Lpt3.Fit")), wantNeed: true, want: Copy},
		{name: "reserved name on Windows", setup: fns(listedAs("MyWorks/Lpt3.Fit"), windows), want: Fail, reason: `name "Lpt3.Fit" is a reserved device name on Windows`},
		{name: "C1 control character", setup: fns(listedAs("MyWorks/target\u0085.fit")), wantNeed: true, want: Copy},
		{name: "C1 control character on Windows", setup: fns(listedAs("MyWorks/target\u0085.fit"), windows), wantNeed: true, want: Copy},
		{name: "DEL on Windows", setup: fns(listedAs("MyWorks/target\x7f.fit"), windows), wantNeed: true, want: Copy},
		{name: "newline elsewhere", setup: fns(listedAs("MyWorks/a\nb.fit")), wantNeed: true, want: Copy},
		{name: "newline on Windows", setup: fns(listedAs("MyWorks/a\nb.fit"), windows), want: Fail, reason: "control character U+000A is not allowed on Windows"},
		{name: "NUL", setup: fns(listedAs("MyWorks/a\x00b.fit")), want: Fail, reason: "control character U+0000 in path"},
		{name: "trailing dot on Windows", setup: fns(listedAs("MyWorks/M 31./a.fit"), windows), want: Fail, reason: `name "M 31." ends in a dot, which Windows cannot store`},
		{name: "zero mtime", setup: fns(listedAt(time.Time{})), want: Fail, reason: "zero mtime, which the state file cannot record"},
		{name: "zero mtime in the state too", setup: fns(listedAt(time.Time{}), recorded(testSize, time.Time{})), want: Fail, reason: "zero mtime, which the state file cannot record"},
		{name: "mtime in year 10000", setup: fns(listedAt(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))), want: Fail, reason: "mtime year 10000 is outside 0 to 9999, which the state file cannot record"},
		{name: "mtime in year 9999 in UTC, 10000 locally", setup: fns(listedAt(time.Date(10000, 1, 1, 8, 0, 0, 0, elsewhere))), want: Wait},
		{name: "mtime in year 10000 in UTC, 9999 locally", setup: fns(listedAt(time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("UTC-2", -2*60*60)))), want: Fail, reason: "mtime year 10000 is outside 0 to 9999, which the state file cannot record"},
		{name: "mtime at the end of year 9999", setup: fns(listedAt(time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC))), want: Wait},
		{name: "mtime in year 0", setup: fns(listedAt(time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC))), wantNeed: true, want: Copy},
		{name: "mtime in year -1", setup: fns(listedAt(time.Date(-1, 12, 31, 23, 59, 59, 0, time.UTC))), want: Fail, reason: "mtime year -1 is outside 0 to 9999, which the state file cannot record"},
		{name: "mtime in year 1, not zero", setup: fns(listedAt(time.Time{}.Add(1))), wantNeed: true, want: Copy},
		{name: "negative size", setup: fns(absent, func(in *Input) { in.Listing[0].Size = -1; in.Previous[0].Size = -1 }), want: Fail, reason: "negative size -1"},
		// Only the first rule broken is named: the path, then the size, then the mtime.
		{name: "negative size and zero mtime", setup: fns(listedAt(time.Time{}), func(in *Input) { in.Listing[0].Size = -1; in.Previous[0].Size = -1 }), want: Fail, reason: "negative size -1"},
		{name: "Windows name and negative size", setup: fns(listedAs("MyWorks/a:b.fit"), windows, func(in *Input) { in.Listing[0].Size = -1; in.Previous[0].Size = -1 }), want: Fail, reason: `character ':' is not allowed on Windows`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := stableInput()
			for _, f := range tc.setup {
				f(&in)
			}
			e := in.Listing[0]

			gotNeed := NeedLocal(in)
			var wantNeed []string
			if tc.wantNeed {
				wantNeed = []string{e.Path}
			}
			if !slices.Equal(gotNeed, wantNeed) {
				t.Errorf("NeedLocal = %q, want %q", gotNeed, wantNeed)
			}

			got, counts := Decide(in)
			want := Decision{Kind: tc.want, Path: e.Path, Size: e.Size, ModTime: e.ModTime, Overwrite: tc.wantOver, Reason: tc.reason}
			if len(got) != 1 || !sameDecision(got[0], want) {
				t.Fatalf("Decide = %v, want [%v]", got, want)
			}
			var wantCounts Counts
			wantCounts.add(tc.want)
			if counts != wantCounts {
				t.Errorf("counts = %+v, want %+v", counts, wantCounts)
			}
		})
	}
}

func fns(f ...func(*Input)) []func(*Input) { return f }

func now(age time.Duration) func(*Input) {
	return func(in *Input) { in.Now = testMtime.Add(age) }
}

func readyAge(d time.Duration) func(*Input) {
	return func(in *Input) { in.ReadyAge = d }
}

func firstPassAge(d time.Duration) func(*Input) {
	return func(in *Input) { in.FirstPassAge = d }
}

func firstPass(in *Input) { in.FirstPass = true }

func windows(in *Input) { in.Windows = true }

func prev(entries ...Entry) func(*Input) {
	return func(in *Input) { in.Previous = entries }
}

func prevChanged(size int64, mtime time.Time) func(*Input) {
	return prev(Entry{Path: testPath, Size: size, ModTime: mtime, Regular: true})
}

func sameDecision(a, b Decision) bool {
	return a.Kind == b.Kind && a.Path == b.Path && a.Size == b.Size && a.ModTime.Equal(b.ModTime) &&
		a.Overwrite == b.Overwrite && a.Reason == b.Reason
}

// TestDecideListing checks a mixed listing: output sorted by path whatever
// the listing order, no decision for non-regular entries, duplicates failed
// once, and the counts.
func TestDecideListing(t *testing.T) {
	old := testMtime
	file := func(p string, size int64) Entry { return Entry{Path: p, Size: size, ModTime: old, Regular: true} }
	dir := func(p string) Entry { return Entry{Path: p, ModTime: old} }
	listing := []Entry{
		file("MyWorks/z.fit", 1),            // copy
		dir("MyWorks"),                      // no decision
		file("MyWorks/dup.fit", 2),          // fail: twice
		file("Autorun/Dark/d.fit", 3),       // done
		file("MyWorks/dup.fit", 2),          //
		dir("MyWorks/dir-and-file"),         // fail: dir and file
		file("MyWorks/dir-and-file", 4),     //
		dir("MyWorks/twice-dir"),            // no decision
		dir("MyWorks/twice-dir"),            //
		file("MyWorks/a.fit", 5),            // adopt
		file("MyWorks/b.fit", 6),            // replace
		file("MyWorks/young.fit", 7),        // wait
		file("MyWorks/../escape.fit", 8),    // fail
		file("darklibrary/changed.fit", 10), // wait: previous size differs
		file("MyWorks/over.fit", 9),         // copy, overwrite
	}
	listing[11].ModTime = old.Add(59 * time.Minute)
	previous := slices.Clone(listing)
	previous[13].Size = 11
	in := Input{
		Listing:  listing,
		Previous: previous,
		State: map[string]Recorded{
			"Autorun/Dark/d.fit": {Size: 3, ModTime: old},
			"MyWorks/b.fit":      {Size: 60, ModTime: old},
		},
		Local: map[string]Local{
			"MyWorks/z.fit":    {},
			"MyWorks/a.fit":    {Exists: true, Regular: true, Size: 5, ModTime: old},
			"MyWorks/over.fit": {Exists: true, Regular: true, Size: 90, ModTime: old},
		},
		Now:          old.Add(time.Hour),
		ReadyAge:     2 * time.Minute,
		FirstPassAge: 10 * time.Minute,
	}

	if got, want := NeedLocal(in), []string{"MyWorks/a.fit", "MyWorks/over.fit", "MyWorks/z.fit"}; !slices.Equal(got, want) {
		t.Errorf("NeedLocal = %q, want %q", got, want)
	}

	got, counts := Decide(in)
	want := []Decision{
		{Kind: Done, Path: "Autorun/Dark/d.fit", Size: 3, ModTime: old},
		{Kind: Fail, Path: "MyWorks/../escape.fit", Size: 8, ModTime: old, Reason: `path element ".."`},
		{Kind: Adopt, Path: "MyWorks/a.fit", Size: 5, ModTime: old},
		{Kind: Replace, Path: "MyWorks/b.fit", Size: 6, ModTime: old},
		{Kind: Fail, Path: "MyWorks/dir-and-file", Reason: "path listed more than once"},
		{Kind: Fail, Path: "MyWorks/dup.fit", Reason: "path listed more than once"},
		{Kind: Copy, Path: "MyWorks/over.fit", Size: 9, ModTime: old, Overwrite: true},
		{Kind: Wait, Path: "MyWorks/young.fit", Size: 7, ModTime: old.Add(59 * time.Minute)},
		{Kind: Copy, Path: "MyWorks/z.fit", Size: 1, ModTime: old},
		{Kind: Wait, Path: "darklibrary/changed.fit", Size: 10, ModTime: old},
	}
	if !slices.EqualFunc(got, want, sameDecision) {
		t.Errorf("Decide:\n got %v\nwant %v", got, want)
	}
	if wantCounts := (Counts{Wait: 2, Done: 1, Copy: 2, Replace: 1, Adopt: 1, Fail: 3}); counts != wantCounts {
		t.Errorf("counts = %+v, want %+v", counts, wantCounts)
	}
}

// TestDecideZeroByte checks that an empty regular file is copied like any
// other: only a negative size fails.
func TestDecideZeroByte(t *testing.T) {
	in := stableInput()
	in.Listing[0].Size = 0
	in.Previous[0].Size = 0
	if got, want := NeedLocal(in), []string{testPath}; !slices.Equal(got, want) {
		t.Errorf("NeedLocal = %q, want %q", got, want)
	}
	absent(&in)
	got, counts := Decide(in)
	want := []Decision{{Kind: Copy, Path: testPath, Size: 0, ModTime: testMtime}}
	if !slices.EqualFunc(got, want, sameDecision) {
		t.Errorf("Decide = %v, want %v", got, want)
	}
	if counts != (Counts{Copy: 1}) {
		t.Errorf("counts = %+v, want %+v", counts, Counts{Copy: 1})
	}
}

// TestCheckModTimeMatchesJSON checks the mtime rule against encoding/json,
// which the state file uses: every mtime it accepts must encode in UTC, and
// every nonzero one it rejects must not.
func TestCheckModTimeMatchesJSON(t *testing.T) {
	for _, mtime := range []time.Time{
		testMtime,
		time.Time{}.Add(1),
		time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(-1, 12, 31, 23, 59, 59, 999999999, time.UTC),
		time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC),
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(10000, 1, 1, 8, 0, 0, 0, elsewhere),
		time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("UTC-2", -2*60*60)),
	} {
		_, err := json.Marshal(mtime.UTC())
		if reason := checkModTime(mtime); (reason == "") != (err == nil) {
			t.Errorf("checkModTime(%v) = %q, but json.Marshal of it in UTC returned %v", mtime, reason, err)
		}
	}
}

func TestDecideEmpty(t *testing.T) {
	got, counts := Decide(Input{})
	if len(got) != 0 || counts != (Counts{}) {
		t.Errorf("Decide(Input{}) = %v, %+v, want none", got, counts)
	}
	if need := NeedLocal(Input{}); len(need) != 0 {
		t.Errorf("NeedLocal(Input{}) = %q, want none", need)
	}
}

func TestKindString(t *testing.T) {
	for k, want := range map[Kind]string{
		Wait: "wait", Done: "done", Copy: "copy", Replace: "replace", Adopt: "adopt", Fail: "fail", Fail + 1: "Kind(6)",
	} {
		if got := k.String(); got != want {
			t.Errorf("Kind(%d).String() = %q, want %q", int(k), got, want)
		}
	}
	if got := fmt.Sprint(Copy); got != "copy" {
		t.Errorf("fmt.Sprint(Copy) = %q, want %q", got, "copy")
	}
}
