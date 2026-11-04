package copier

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// replaceSync makes syncFile call fn instead, for the rest of the test.
func replaceSync(t *testing.T, fn func(*os.File) error) {
	t.Helper()
	orig := syncFile
	syncFile = fn
	t.Cleanup(func() { syncFile = orig })
}

// relName returns the name of f relative to dest, with forward slashes.
func relName(t *testing.T, dest string, f *os.File) string {
	t.Helper()
	rel, err := filepath.Rel(dest, f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(rel)
}

// syncEvent is one fsync, with what the destination held when it happened.
type syncEvent struct {
	name     string // relative to the destination
	temp     bool   // the temporary file exists
	inPlace  bool   // the final file exists
	recorded bool   // the state has a record
}

// A download fsyncs its temporary file before the rename, then the file's
// folder after it, and only then appends the record. On Windows the folder
// is not fsynced.
func TestDownloadSyncOrder(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		prepare string // a folder made before the download
	}{
		{name: "new folders", path: "MyWorks/M 31/a.fit"},
		{name: "existing folders", path: "MyWorks/M 31/a.fit", prepare: "MyWorks/M 31"},
		{name: "file at the top", path: "d.fit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t, func(dest string) {
				if tc.prepare != "" {
					if err := os.MkdirAll(filepath.Join(dest, filepath.FromSlash(tc.prepare)), 0o777); err != nil {
						t.Fatal(err)
					}
				}
			})
			c := fx.copier(t)
			tmp := path.Join(path.Dir(tc.path), plan.TempName(tc.path))
			exists := func(p string) bool {
				_, err := os.Lstat(filepath.Join(fx.dest, filepath.FromSlash(p)))
				return err == nil
			}
			var got []syncEvent
			replaceSync(t, func(f *os.File) error {
				_, recorded := fx.st.Lookup(tc.path)
				got = append(got, syncEvent{name: relName(t, fx.dest, f), temp: exists(tmp), inPlace: exists(tc.path), recorded: recorded})
				return f.Sync()
			})
			results := run(c, t.Context(), device(map[string]string{tc.path: "abc"}), 1, []plan.Decision{decision(plan.Copy, tc.path, 3)})
			if len(results) != 1 || results[0].Err != nil {
				t.Fatalf("got results %+v, want one success", results)
			}
			want := []syncEvent{{name: tmp, temp: true}}
			if runtime.GOOS != "windows" {
				want = append(want, syncEvent{name: path.Dir(tc.path), inPlace: true})
			}
			if !slices.Equal(got, want) {
				t.Errorf("fsynced\n%+v\nwant\n%+v", got, want)
			}
			if _, ok := fx.st.Lookup(tc.path); !ok {
				t.Errorf("%s is not recorded", tc.path)
			}
		})
	}
}

// An adoption fsyncs nothing; it only reads the file and records it.
func TestAdoptSyncsNothing(t *testing.T) {
	const p = "MyWorks/a.fit"
	fx := newFixture(t, func(dest string) { writeLocal(t, dest, p, "abc") })
	c := fx.copier(t)
	replaceSync(t, func(f *os.File) error {
		t.Errorf("fsynced %s", relName(t, fx.dest, f))
		return f.Sync()
	})
	results := run(c, t.Context(), device(nil), 1, []plan.Decision{decision(plan.Adopt, p, 3)})
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("got results %+v, want one success", results)
	}
	if _, ok := fx.st.Lookup(p); !ok {
		t.Errorf("%s is not recorded", p)
	}
}

// A failed fsync, or a failed close after one, fails the file and records
// nothing. Before the rename the final file is not there; after it, it is.
func TestDownloadSyncFailure(t *testing.T) {
	errSync := errors.New("injected fsync error")
	isTemp := func(name string) bool { return plan.IsTempName(path.Base(name)) }
	isFolder := func(name string) bool { return name == "MyWorks" }
	for _, tc := range []struct {
		name    string
		fail    func(name string) bool
		close   bool // close the file instead, so that its own close fails
		want    string
		inPlace bool
	}{
		{name: "temporary file", fail: isTemp, want: "fsync the temporary file: "},
		{name: "temporary file close", fail: isTemp, close: true, want: "close the temporary file"},
		{name: "folder", fail: isFolder, want: "fsync the folder: ", inPlace: true},
		{name: "folder close", fail: isFolder, close: true, want: "fsync the folder: ", inPlace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.inPlace && runtime.GOOS == "windows" {
				t.Skip("folders are not fsynced on Windows")
			}
			fx := newFixture(t, nil)
			c := fx.copier(t)
			replaceSync(t, func(f *os.File) error {
				if !tc.fail(relName(t, fx.dest, f)) {
					return f.Sync()
				}
				if tc.close {
					return f.Close()
				}
				return errSync
			})
			results := run(c, t.Context(), device(map[string]string{"MyWorks/a.fit": "abc"}), 1,
				[]plan.Decision{decision(plan.Copy, "MyWorks/a.fit", 3)})
			fx.checkFailed(t, results, tc.want)
			wantErr := errSync
			if tc.close {
				wantErr = fs.ErrClosed
			}
			if !errors.Is(results[0].Err, wantErr) {
				t.Errorf("got err %v, want one wrapping %v", results[0].Err, wantErr)
			}
			if tc.inPlace {
				fx.checkFile(t, "MyWorks/a.fit", "abc")
			} else {
				fx.checkAbsent(t, "MyWorks/a.fit")
			}
			if got := fx.records(t); len(got) != 0 {
				t.Errorf("state records: %q", got)
			}
		})
	}
}

// A cancel between a file's rename and its record still records the file:
// it is complete under its final name. The cancel comes while its folder is
// fsynced, after the rename and before the append.
func TestCancelBeforeRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("folders are not fsynced on Windows")
	}
	const p = "MyWorks/a.fit"
	fx := newFixture(t, nil)
	c := fx.copier(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	canceled := false
	replaceSync(t, func(f *os.File) error {
		if relName(t, fx.dest, f) == path.Dir(p) {
			// The hook runs on a worker, so only non-fatal checks here.
			fx.checkFile(t, p, "abc")
			if b, err := os.ReadFile(fx.statePath); err != nil || strings.Count(string(b), "\n") != 1 {
				t.Errorf("state before the record: %q (err %v), want only the header", b, err)
			}
			cancel()
			canceled = true
		}
		return f.Sync()
	})
	results := run(c, ctx, device(map[string]string{p: "abc"}), 1, []plan.Decision{decision(plan.Copy, p, 3)})
	if !canceled {
		t.Fatalf("the folder of %s was not fsynced", p)
	}
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("got results %+v, want one success", results)
	}
	if got := fx.records(t); len(got) != 1 {
		t.Errorf("state records: %q, want one", got)
	}
	if _, ok := fx.st.Lookup(p); !ok {
		t.Errorf("%s is not recorded after the cancel", p)
	}
	fx.checkNoTemp(t)
}

// A file renamed into place before a cancel is still recorded, though the
// pass was cut short.
func TestCancelAfterRename(t *testing.T) {
	fx := newFixture(t, nil)
	c := fx.copier(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sess := device(map[string]string{"MyWorks/a.fit": "abc", "MyWorks/b.fit": "abc"})
	sess.Faults = map[string]remote.Fault{"MyWorks/b.fit": {After: 1, Stall: cancel}}
	results := run(c, ctx, sess, 1, []plan.Decision{decision(plan.Copy, "MyWorks/a.fit", 3), decision(plan.Copy, "MyWorks/b.fit", 3)})
	if results[0].Err != nil || !errors.Is(results[1].Err, context.Canceled) {
		t.Errorf("got results %+v, want the first copied and the second canceled", results)
	}
	if _, ok := fx.st.Lookup("MyWorks/a.fit"); !ok {
		t.Errorf("MyWorks/a.fit is not recorded after the cancel")
	}
	fx.checkAbsent(t, "MyWorks/b.fit")
}
