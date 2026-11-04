package plan

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// TestDecideClashes lists files that are stable, absent locally and, unless
// a case says otherwise, not in the state and of size 1, so each one that
// doesn't clash is a Copy. Each case runs with and without the Windows
// rules unless it names one.
func TestDecideClashes(t *testing.T) {
	old := testMtime
	young := testMtime.Add(59 * time.Minute)
	type file struct {
		path  string
		size  int64
		mtime time.Time
	}
	f := func(p string) file { return file{p, 1, old} }
	clash := func(p, reason string) Decision {
		return Decision{Kind: Fail, Path: p, Size: 1, ModTime: old, Reason: reason}
	}
	copied := func(p string) Decision { return Decision{Kind: Copy, Path: p, Size: 1, ModTime: old} }
	tests := []struct {
		name    string
		listing []file
		dirs    []string
		state   []string
		windows []bool // nil for both
		// newDirs are in this listing but not the previous one, staleDirs
		// in the previous listing only. firstPass sets FirstPass with no
		// previous listing.
		newDirs   []string
		staleDirs []string
		firstPass bool
		want      []Decision
		counts    Counts
	}{
		{
			name:    "case-only file names",
			listing: []file{f("MyWorks/M 31/a.fit"), f("MyWorks/M 31/A.fit")},
			want: []Decision{
				clash("MyWorks/M 31/A.fit", `name clash: "MyWorks/M 31/A.fit" and "MyWorks/M 31/a.fit" differ only in case or Unicode normalization`),
				clash("MyWorks/M 31/a.fit", `name clash: "MyWorks/M 31/a.fit" and "MyWorks/M 31/A.fit" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 2},
		},
		{
			name:    "NFC and NFD",
			listing: []file{f("MyWorks/\u00e9.fit"), f("MyWorks/e\u0301.fit")},
			want: []Decision{
				clash("MyWorks/e\u0301.fit", `name clash: "MyWorks/e\u0301.fit" and "MyWorks/\u00e9.fit" differ only in case or Unicode normalization`),
				clash("MyWorks/\u00e9.fit", `name clash: "MyWorks/\u00e9.fit" and "MyWorks/e\u0301.fit" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 2},
		},
		{
			// Folding a non-NFC name can give a different result from folding
			// its NFC form, so the key normalizes before it folds.
			name:    "NFC before case folding",
			listing: []file{f("MyWorks/\u1fc4.fit"), f("MyWorks/\u03b7\u0345\u0301.fit")},
			want: []Decision{
				clash("MyWorks/\u03b7\u0345\u0301.fit", `name clash: "MyWorks/\u03b7\u0345\u0301.fit" and "MyWorks/\u1fc4.fit" differ only in case or Unicode normalization`),
				clash("MyWorks/\u1fc4.fit", `name clash: "MyWorks/\u1fc4.fit" and "MyWorks/\u03b7\u0345\u0301.fit" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 2},
		},
		{
			// Folding follows the result of NFC: "\u00df" followed by U+0301
			// folds to "ss" and U+0301, which NFC composes into "s\u015b".
			name:    "NFC after case folding",
			listing: []file{f("MyWorks/\u00df\u0301.fit"), f("MyWorks/s\u015b.fit")},
			want: []Decision{
				clash("MyWorks/s\u015b.fit", `name clash: "MyWorks/s\u015b.fit" and "MyWorks/\u00df\u0301.fit" differ only in case or Unicode normalization`),
				clash("MyWorks/\u00df\u0301.fit", `name clash: "MyWorks/\u00df\u0301.fit" and "MyWorks/s\u015b.fit" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 2},
		},
		{
			// Full case folding, so "\u00df" and "\u1e9e" are "ss". Each
			// reason names the smallest other spelling in byte order and
			// counts the rest.
			name:    "six spellings",
			listing: []file{f("MyWorks/ss"), f("MyWorks/SS"), f("MyWorks/Ss"), f("MyWorks/sS"), f("MyWorks/\u00df"), f("MyWorks/\u1e9e")},
			want: []Decision{
				clash("MyWorks/SS", `name clash: "MyWorks/SS" and "MyWorks/Ss" and 4 others differ only in case or Unicode normalization`),
				clash("MyWorks/Ss", `name clash: "MyWorks/Ss" and "MyWorks/SS" and 4 others differ only in case or Unicode normalization`),
				clash("MyWorks/sS", `name clash: "MyWorks/sS" and "MyWorks/SS" and 4 others differ only in case or Unicode normalization`),
				clash("MyWorks/ss", `name clash: "MyWorks/ss" and "MyWorks/SS" and 4 others differ only in case or Unicode normalization`),
				clash("MyWorks/\u00df", `name clash: "MyWorks/\u00df" and "MyWorks/SS" and 4 others differ only in case or Unicode normalization`),
				clash("MyWorks/\u1e9e", `name clash: "MyWorks/\u1e9e" and "MyWorks/SS" and 4 others differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 6},
		},
		{
			name:    "case and normalization together, three spellings",
			listing: []file{f("MyWorks/\u00e9"), f("MyWorks/\u00c9"), f("MyWorks/E\u0301"), f("MyWorks/e")},
			want: []Decision{
				clash("MyWorks/E\u0301", `name clash: "MyWorks/E\u0301" and "MyWorks/\u00c9" and 1 other differ only in case or Unicode normalization`),
				copied("MyWorks/e"),
				clash("MyWorks/\u00c9", `name clash: "MyWorks/\u00c9" and "MyWorks/E\u0301" and 1 other differ only in case or Unicode normalization`),
				clash("MyWorks/\u00e9", `name clash: "MyWorks/\u00e9" and "MyWorks/E\u0301" and 1 other differ only in case or Unicode normalization`),
			},
			counts: Counts{Copy: 1, Fail: 3},
		},
		{
			name:    "folder alias",
			listing: []file{f("MyWorks/M 31/a.fit"), f("MyWorks/m 31/b.fit"), f("MyWorks/M 42/a.fit")},
			want: []Decision{
				clash("MyWorks/M 31/a.fit", `name clash: "MyWorks/M 31" and "MyWorks/m 31" differ only in case or Unicode normalization`),
				copied("MyWorks/M 42/a.fit"),
				clash("MyWorks/m 31/b.fit", `name clash: "MyWorks/m 31" and "MyWorks/M 31" differ only in case or Unicode normalization`),
			},
			counts: Counts{Copy: 1, Fail: 2},
		},
		{
			// "MyWorks/M 31" and "MyWorks/m 31" clash too, but "MyWorks" is
			// named.
			name:    "the shallowest clash is named",
			listing: []file{f("MyWorks/M 31/a.fit"), f("myworks/m 31/A.fit"), f("MyWorks/m 31/c.fit")},
			want: []Decision{
				clash("MyWorks/M 31/a.fit", `name clash: "MyWorks" and "myworks" differ only in case or Unicode normalization`),
				clash("MyWorks/m 31/c.fit", `name clash: "MyWorks" and "myworks" differ only in case or Unicode normalization`),
				clash("myworks/m 31/A.fit", `name clash: "myworks" and "MyWorks" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 3},
		},
		{
			name:    "file and folder",
			listing: []file{f("MyWorks/A"), f("MyWorks/a/b.fit")},
			want: []Decision{
				clash("MyWorks/A", `name clash: "MyWorks/A" and "MyWorks/a" differ only in case or Unicode normalization`),
				clash("MyWorks/a/b.fit", `name clash: "MyWorks/a" and "MyWorks/A" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 2},
		},
		{
			name:    "a recorded path that is not listed",
			listing: []file{f("MyWorks/M 31/a.fit"), f("MyWorks/b.fit")},
			state:   []string{"MyWorks/m 31/a.fit"},
			want: []Decision{
				clash("MyWorks/M 31/a.fit", `name clash: "MyWorks/M 31" and "MyWorks/m 31" differ only in case or Unicode normalization`),
				copied("MyWorks/b.fit"),
			},
			counts: Counts{Copy: 1, Fail: 1},
		},
		{
			name:    "recorded paths that clash only with each other",
			listing: []file{f("MyWorks/b.fit")},
			state:   []string{"MyWorks/a.fit", "MyWorks/A.fit"},
			want:    []Decision{copied("MyWorks/b.fit")},
			counts:  Counts{Copy: 1},
		},
		{
			name:    "a recorded path with the same spelling",
			listing: []file{f("MyWorks/M 31/a.fit")},
			state:   []string{"MyWorks/M 31/a.fit", "MyWorks/M 31/b.fit"},
			want:    []Decision{{Kind: Done, Path: "MyWorks/M 31/a.fit", Size: 1, ModTime: old}},
			counts:  Counts{Done: 1},
		},
		{
			name:    "a done file and a waiting one are failed too",
			listing: []file{f("MyWorks/X.fit"), {"MyWorks/x.fit", 1, young}, f("MyWorks/y.fit")},
			state:   []string{"MyWorks/X.fit", "MyWorks/y.fit"},
			want: []Decision{
				clash("MyWorks/X.fit", `name clash: "MyWorks/X.fit" and "MyWorks/x.fit" differ only in case or Unicode normalization`),
				{Kind: Fail, Path: "MyWorks/x.fit", Size: 1, ModTime: young, Reason: `name clash: "MyWorks/x.fit" and "MyWorks/X.fit" differ only in case or Unicode normalization`},
				{Kind: Done, Path: "MyWorks/y.fit", Size: 1, ModTime: old},
			},
			counts: Counts{Done: 1, Fail: 2},
		},
		{
			// A path that fails for its own reason keeps it, and the clash is
			// added, so the other spelling is named.
			name:    "a path listed twice keeps its own reason",
			listing: []file{f("MyWorks/X.fit"), f("MyWorks/X.fit"), f("MyWorks/x.fit")},
			want: []Decision{
				{Kind: Fail, Path: "MyWorks/X.fit", Reason: `path listed more than once; name clash: "MyWorks/X.fit" and "MyWorks/x.fit" differ only in case or Unicode normalization`},
				clash("MyWorks/x.fit", `name clash: "MyWorks/x.fit" and "MyWorks/X.fit" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 2},
		},
		{
			name:    "a negative size and a valid spelling",
			listing: []file{{"MyWorks/X.fit", -1, old}, f("MyWorks/x.fit")},
			want: []Decision{
				{Kind: Fail, Path: "MyWorks/X.fit", Size: -1, ModTime: old, Reason: `negative size -1; name clash: "MyWorks/X.fit" and "MyWorks/x.fit" differ only in case or Unicode normalization`},
				clash("MyWorks/x.fit", `name clash: "MyWorks/x.fit" and "MyWorks/X.fit" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 2},
		},
		{
			name:    "a zero mtime and a valid spelling",
			listing: []file{{"MyWorks/M 31/a.fit", 1, time.Time{}}, f("MyWorks/m 31/b.fit")},
			want: []Decision{
				{Kind: Fail, Path: "MyWorks/M 31/a.fit", Size: 1, Reason: `zero mtime, which the state file cannot record; name clash: "MyWorks/M 31" and "MyWorks/m 31" differ only in case or Unicode normalization`},
				clash("MyWorks/m 31/b.fit", `name clash: "MyWorks/m 31" and "MyWorks/M 31" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 2},
		},
		{
			name:    "an mtime the state cannot record and a done spelling",
			listing: []file{{"MyWorks/X.fit", 1, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}, f("MyWorks/x.fit")},
			state:   []string{"MyWorks/x.fit"},
			want: []Decision{
				{Kind: Fail, Path: "MyWorks/X.fit", Size: 1, ModTime: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), Reason: `mtime year 10000 is outside 0 to 9999, which the state file cannot record; name clash: "MyWorks/X.fit" and "MyWorks/x.fit" differ only in case or Unicode normalization`},
				clash("MyWorks/x.fit", `name clash: "MyWorks/x.fit" and "MyWorks/X.fit" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 2},
		},
		{
			// Windows can't store the colon, but the name still takes part, so
			// its valid folder alias fails too.
			name:    "a name Windows cannot store and a valid folder alias",
			listing: []file{f("MyWorks/M 31/b:c"), f("MyWorks/m 31/a.fit")},
			windows: []bool{true},
			want: []Decision{
				clash("MyWorks/M 31/b:c", `character ':' is not allowed on Windows; name clash: "MyWorks/M 31" and "MyWorks/m 31" differ only in case or Unicode normalization`),
				clash("MyWorks/m 31/a.fit", `name clash: "MyWorks/m 31" and "MyWorks/M 31" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 2},
		},
		{
			name:    "the same names without the Windows rules",
			listing: []file{f("MyWorks/M 31/b:c"), f("MyWorks/m 31/a.fit")},
			windows: []bool{false},
			want: []Decision{
				clash("MyWorks/M 31/b:c", `name clash: "MyWorks/M 31" and "MyWorks/m 31" differ only in case or Unicode normalization`),
				clash("MyWorks/m 31/a.fit", `name clash: "MyWorks/m 31" and "MyWorks/M 31" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 2},
		},
		{
			name:    "a recorded name Windows cannot store",
			listing: []file{f("MyWorks/m 31/a.fit"), f("MyWorks/b.fit")},
			state:   []string{"MyWorks/M 31/b:c"},
			want: []Decision{
				copied("MyWorks/b.fit"),
				clash("MyWorks/m 31/a.fit", `name clash: "MyWorks/m 31" and "MyWorks/M 31" differ only in case or Unicode normalization`),
			},
			counts: Counts{Copy: 1, Fail: 1},
		},
		{
			name:    "two names Windows cannot store",
			listing: []file{f("MyWorks/B:C"), f("MyWorks/b:c")},
			windows: []bool{true},
			want: []Decision{
				clash("MyWorks/B:C", `character ':' is not allowed on Windows; name clash: "MyWorks/B:C" and "MyWorks/b:c" differ only in case or Unicode normalization`),
				clash("MyWorks/b:c", `character ':' is not allowed on Windows; name clash: "MyWorks/b:c" and "MyWorks/B:C" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 2},
		},
		{
			name:    "two names Windows cannot store, without the Windows rules",
			listing: []file{f("MyWorks/B:C"), f("MyWorks/b:c")},
			windows: []bool{false},
			want: []Decision{
				clash("MyWorks/B:C", `name clash: "MyWorks/B:C" and "MyWorks/b:c" differ only in case or Unicode normalization`),
				clash("MyWorks/b:c", `name clash: "MyWorks/b:c" and "MyWorks/B:C" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 2},
		},
		{
			// These never become local names, so they keep their own reason
			// and make nothing else fail: a recorded "M 31/../x" doesn't put
			// "M 31" in the namespace.
			name: "lexically unsafe paths keep their own reason and clash with nothing",
			listing: []file{
				f("MyWorks/M 31/../a.fit"), f("MyWorks/m 31/a.fit"), f("MyWorks/A\x00"), f("MyWorks/a\x00"),
				f("t/.seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tmp"), f("T/.Seestar-Copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.Tmp"), f("MyWorks/N 1/../b.fit"), f("MyWorks/N 1/c.fit"), f("MyWorks/n 1/d.fit"),
			},
			dirs:  []string{"MyWorks/M 31/.."},
			state: []string{"MyWorks/X/../y.fit", "MyWorks/z\xff"},
			want: []Decision{
				{Kind: Fail, Path: "MyWorks/A\x00", Size: 1, ModTime: old, Reason: "control character U+0000 in path"},
				{Kind: Fail, Path: "MyWorks/M 31/../a.fit", Size: 1, ModTime: old, Reason: `path element ".."`},
				{Kind: Fail, Path: "MyWorks/N 1/../b.fit", Size: 1, ModTime: old, Reason: `path element ".."`},
				clash("MyWorks/N 1/c.fit", `name clash: "MyWorks/N 1" and "MyWorks/n 1" differ only in case or Unicode normalization`),
				{Kind: Fail, Path: "MyWorks/a\x00", Size: 1, ModTime: old, Reason: "control character U+0000 in path"},
				copied("MyWorks/m 31/a.fit"),
				clash("MyWorks/n 1/d.fit", `name clash: "MyWorks/n 1" and "MyWorks/N 1" differ only in case or Unicode normalization`),
				{Kind: Fail, Path: "T/.Seestar-Copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.Tmp", Size: 1, ModTime: old, Reason: `name ".Seestar-Copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.Tmp" is reserved for temporary files`},
				{Kind: Fail, Path: "t/.seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tmp", Size: 1, ModTime: old, Reason: `name ".seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tmp" is reserved for temporary files`},
			},
			counts: Counts{Copy: 1, Fail: 8},
		},
		{
			// Listed folders take part in the comparison but get no decision.
			name:    "a file and a listed folder",
			listing: []file{f("MyWorks/A")},
			dirs:    []string{"MyWorks/a"},
			want: []Decision{
				clash("MyWorks/A", `name clash: "MyWorks/A" and "MyWorks/a" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 1},
		},
		{
			name:    "an empty folder alias",
			listing: []file{f("MyWorks/m 31/a.fit")},
			dirs:    []string{"MyWorks", "MyWorks/m 31", "MyWorks/M 31"},
			want: []Decision{
				clash("MyWorks/m 31/a.fit", `name clash: "MyWorks/m 31" and "MyWorks/M 31" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 1},
		},
		{
			name:    "a done file and a listed folder",
			listing: []file{f("MyWorks/X.fit"), f("MyWorks/y.fit")},
			dirs:    []string{"MyWorks/x.fit"},
			state:   []string{"MyWorks/X.fit", "MyWorks/y.fit"},
			want: []Decision{
				clash("MyWorks/X.fit", `name clash: "MyWorks/X.fit" and "MyWorks/x.fit" differ only in case or Unicode normalization`),
				{Kind: Done, Path: "MyWorks/y.fit", Size: 1, ModTime: old},
			},
			counts: Counts{Done: 1, Fail: 1},
		},
		{
			// The comparison uses this pass's listing, not the previous one.
			name:    "a folder new in this listing",
			listing: []file{f("MyWorks/A")},
			newDirs: []string{"MyWorks/a"},
			want: []Decision{
				clash("MyWorks/A", `name clash: "MyWorks/A" and "MyWorks/a" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 1},
		},
		{
			name:    "a done file and a folder new in this listing",
			listing: []file{f("MyWorks/X.fit"), f("MyWorks/y.fit")},
			newDirs: []string{"MyWorks/x.fit"},
			state:   []string{"MyWorks/X.fit", "MyWorks/y.fit"},
			want: []Decision{
				clash("MyWorks/X.fit", `name clash: "MyWorks/X.fit" and "MyWorks/x.fit" differ only in case or Unicode normalization`),
				{Kind: Done, Path: "MyWorks/y.fit", Size: 1, ModTime: old},
			},
			counts: Counts{Done: 1, Fail: 1},
		},
		{
			name:      "a folder on the first pass",
			listing:   []file{f("MyWorks/A")},
			dirs:      []string{"MyWorks/a"},
			firstPass: true,
			want: []Decision{
				clash("MyWorks/A", `name clash: "MyWorks/A" and "MyWorks/a" differ only in case or Unicode normalization`),
			},
			counts: Counts{Fail: 1},
		},
		{
			name:      "a done file and a folder on the first pass",
			listing:   []file{f("MyWorks/X.fit"), f("MyWorks/y.fit")},
			dirs:      []string{"MyWorks/x.fit"},
			state:     []string{"MyWorks/X.fit", "MyWorks/y.fit"},
			firstPass: true,
			want: []Decision{
				clash("MyWorks/X.fit", `name clash: "MyWorks/X.fit" and "MyWorks/x.fit" differ only in case or Unicode normalization`),
				{Kind: Done, Path: "MyWorks/y.fit", Size: 1, ModTime: old},
			},
			counts: Counts{Done: 1, Fail: 1},
		},
		{
			// A folder gone since the previous pass is not in the namespace.
			name:      "a folder only in the previous listing",
			listing:   []file{f("MyWorks/A")},
			staleDirs: []string{"MyWorks/a"},
			want:      []Decision{copied("MyWorks/A")},
			counts:    Counts{Copy: 1},
		},
		{
			name:      "a done file and a folder only in the previous listing",
			listing:   []file{f("MyWorks/X.fit"), f("MyWorks/y.fit")},
			staleDirs: []string{"MyWorks/x.fit"},
			state:     []string{"MyWorks/X.fit", "MyWorks/y.fit"},
			want: []Decision{
				{Kind: Done, Path: "MyWorks/X.fit", Size: 1, ModTime: old},
				{Kind: Done, Path: "MyWorks/y.fit", Size: 1, ModTime: old},
			},
			counts: Counts{Done: 2},
		},
		{
			name:    "listed folders with the file's spelling, and folder aliases with no file",
			listing: []file{f("MyWorks/m 31/a.fit")},
			dirs:    []string{"MyWorks", "MyWorks/m 31", "MyWorks/P", "MyWorks/p"},
			want:    []Decision{copied("MyWorks/m 31/a.fit")},
			counts:  Counts{Copy: 1},
		},
		{
			name: "Unicode names that differ",
			listing: []file{
				f("\u00dcn\u00efc\u00f8d\u00e9/\u661f\u96f2.fit"), f("MyWorks/\u00e9.fit"), f("MyWorks/e.fit"), f("MyWorks/\u0131.fit"),
				f("MyWorks/i.fit"), f("MyWorks/\u00e8.fit"), f("MyWorks/a/b.fit"), f("MyWorks/ab.fit"),
			},
			want: []Decision{
				copied("MyWorks/a/b.fit"), copied("MyWorks/ab.fit"), copied("MyWorks/e.fit"), copied("MyWorks/i.fit"),
				copied("MyWorks/\u00e8.fit"), copied("MyWorks/\u00e9.fit"), copied("MyWorks/\u0131.fit"), copied("\u00dcn\u00efc\u00f8d\u00e9/\u661f\u96f2.fit"),
			},
			counts: Counts{Copy: 8},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{
				State:        map[string]Recorded{},
				Local:        map[string]Local{},
				Now:          old.Add(time.Hour),
				ReadyAge:     2 * time.Minute,
				FirstPassAge: 10 * time.Minute,
			}
			for _, x := range tc.listing {
				in.Listing = append(in.Listing, Entry{Path: x.path, Size: x.size, ModTime: x.mtime, Regular: true})
				in.Local[x.path] = Local{}
			}
			for _, d := range tc.dirs {
				in.Listing = append(in.Listing, Entry{Path: d, ModTime: old})
			}
			in.Previous = slices.Clone(in.Listing)
			for _, d := range tc.newDirs {
				in.Listing = append(in.Listing, Entry{Path: d, ModTime: old})
			}
			for _, d := range tc.staleDirs {
				in.Previous = append(in.Previous, Entry{Path: d, ModTime: old})
			}
			if tc.firstPass {
				in.FirstPass, in.Previous = true, nil
			}
			for _, p := range tc.state {
				in.State[p] = Recorded{Size: 1, ModTime: old}
			}

			var wantNeed []string
			for _, d := range tc.want {
				if d.Kind == Copy {
					wantNeed = append(wantNeed, d.Path)
				}
			}
			platforms := tc.windows
			if platforms == nil {
				platforms = []bool{false, true}
			}
			for _, w := range platforms {
				in.Windows = w
				if got := NeedLocal(in); !slices.Equal(got, wantNeed) {
					t.Errorf("Windows %v: NeedLocal = %q, want %q", w, got, wantNeed)
				}
				got, counts := Decide(in)
				if !slices.EqualFunc(got, tc.want, sameDecision) {
					t.Errorf("Windows %v: Decide:\n got %s\nwant %s", w, showDecisions(got), showDecisions(tc.want))
				}
				if counts != tc.counts {
					t.Errorf("Windows %v: counts = %+v, want %+v", w, counts, tc.counts)
				}
			}
		})
	}
}

// aliasInput lists stable files that are absent locally and not in the
// state.
func aliasInput(paths []string) Input {
	in := Input{
		State:        map[string]Recorded{},
		Local:        map[string]Local{},
		Now:          testMtime.Add(time.Hour),
		ReadyAge:     2 * time.Minute,
		FirstPassAge: 10 * time.Minute,
	}
	for _, p := range paths {
		in.Listing = append(in.Listing, Entry{Path: p, Size: 1, ModTime: testMtime, Regular: true})
		in.Local[p] = Local{}
	}
	in.Previous = slices.Clone(in.Listing)
	return in
}

// TestClashLargeGroup spells one folder 2048 ways. Every file fails, and
// each reason names one other spelling, the smallest in byte order, and
// counts the rest, so a reason's length doesn't grow with the group.
func TestClashLargeGroup(t *testing.T) {
	const n = 2048
	folders := make([]string, n)
	paths := make([]string, n)
	for i := range n {
		name := []byte("abcdefghijklmnopq")
		for bit := range name {
			if i&(1<<bit) != 0 {
				name[bit] -= 'a' - 'A'
			}
		}
		folders[i] = "MyWorks/" + string(name)
		paths[i] = folders[i] + "/a.fit"
	}
	sorted := slices.Clone(folders)
	slices.Sort(sorted)
	if sorted[0] != "MyWorks/ABCDEFGHIJKlmnopq" || sorted[1] != "MyWorks/ABCDEFGHIJklmnopq" {
		t.Fatalf("smallest spellings are %+q and %+q", sorted[0], sorted[1])
	}

	in := aliasInput(paths)
	for _, p := range []bool{false, true} {
		in.Windows = p
		if got := NeedLocal(in); len(got) != 0 {
			t.Errorf("Windows %v: NeedLocal = %d paths, want none", p, len(got))
		}
		ds, counts := Decide(in)
		if counts != (Counts{Fail: n}) {
			t.Errorf("Windows %v: counts = %+v, want %+v", p, counts, Counts{Fail: n})
		}
		bad, longest := 0, 0
		for _, d := range ds {
			folder := strings.TrimSuffix(d.Path, "/a.fit")
			other := sorted[0]
			if folder == other {
				other = sorted[1]
			}
			want := fmt.Sprintf("name clash: %+q and %+q and 2046 others differ only in case or Unicode normalization", folder, other)
			longest = max(longest, len(d.Reason))
			if d.Kind != Fail || d.Reason != want {
				if bad == 0 {
					t.Errorf("Windows %v: %+q got %v %.200q, want Fail %+q", p, d.Path, d.Kind, d.Reason, want)
				}
				bad++
			}
		}
		if bad > 0 || longest > 160 {
			t.Errorf("Windows %v: %d of %d decisions wrong, longest reason %d bytes", p, bad, len(ds), longest)
		}
	}
}

// TestClashReasonShared checks that the files in one clashing folder share
// one reason string rather than a copy each.
func TestClashReasonShared(t *testing.T) {
	ds, _ := Decide(aliasInput([]string{"MyWorks/M 31/a.fit", "MyWorks/M 31/b.fit", "MyWorks/m 31/c.fit"}))
	if len(ds) != 3 || ds[0].Reason == "" || ds[0].Reason != ds[1].Reason {
		t.Fatalf("Decide:%s", showDecisions(ds))
	}
	if unsafe.StringData(ds[0].Reason) != unsafe.StringData(ds[1].Reason) {
		t.Error("the files of one folder have separate copies of its clash reason")
	}
}

// showDecisions formats decisions with their paths and reasons escaped, so
// spellings that look alike can be told apart.
func showDecisions(ds []Decision) string {
	s := ""
	for _, d := range ds {
		s += fmt.Sprintf("\n\t%v %+q size %d mtime %v overwrite %v reason %+q", d.Kind, d.Path, d.Size, d.ModTime, d.Overwrite, d.Reason)
	}
	return s
}
