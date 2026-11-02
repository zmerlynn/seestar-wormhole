package copier

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// deviceMTime is the device mtime of the test files.
var deviceMTime = time.Date(2026, 10, 29, 3, 31, 19, 0, time.UTC)

const (
	// The SHA-256 of "abc" and of no bytes.
	sumABC   = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	sumEmpty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// device returns a fake session serving files, each with deviceMTime.
func device(files map[string]string) *remote.Fake {
	fsys := fstest.MapFS{}
	for p, data := range files {
		fsys[p] = &fstest.MapFile{Data: []byte(data), ModTime: deviceMTime}
	}
	return remote.NewFake(fsys)
}

func decision(kind plan.Kind, p string, size int64) plan.Decision {
	return plan.Decision{Kind: kind, Path: p, Size: size, ModTime: deviceMTime}
}

// run calls c.Run and returns the results it was given, in the order of
// their paths in ds.
func run(c *Copier, ctx context.Context, sess remote.Session, workers int, ds []plan.Decision) []Result {
	var results []Result
	c.Run(ctx, sess, workers, ds, func(r Result) { results = append(results, r) })
	slices.SortStableFunc(results, func(a, b Result) int {
		at := slices.IndexFunc(ds, func(d plan.Decision) bool { return d.Path == a.Decision.Path })
		bt := slices.IndexFunc(ds, func(d plan.Decision) bool { return d.Path == b.Decision.Path })
		return at - bt
	})
	return results
}

// records returns the lines of the state file after its header.
func (fx fixture) records(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(fx.statePath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(b), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], `{"v":1,`) || lines[len(lines)-1] != "" {
		t.Fatalf("state file is not a header and whole lines:\n%s", b)
	}
	return lines[1 : len(lines)-1]
}

// checkFile checks the content and mtime of share path p in the destination.
func (fx fixture) checkFile(t *testing.T, p, want string) {
	t.Helper()
	name := filepath.Join(fx.dest, filepath.FromSlash(p))
	got, err := os.ReadFile(name)
	if err != nil {
		t.Errorf("read %s: %v", p, err)
		return
	}
	if string(got) != want {
		t.Errorf("%s holds %q, want %q", p, got, want)
	}
	if fi, err := os.Stat(name); err != nil || !fi.ModTime().Equal(deviceMTime) {
		t.Errorf("%s mtime: got %v (err %v), want %v", p, fi.ModTime(), err, deviceMTime)
	}
}

// checkAbsent checks that share path p is not in the destination.
func (fx fixture) checkAbsent(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(fx.dest, filepath.FromSlash(p))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s in the destination: got err %v, want not exist", p, err)
	}
}

// temps returns, with forward slashes, every entry under the destination
// whose name starts like a temporary file's.
func (fx fixture) temps(t *testing.T) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(fx.dest, func(name string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(de.Name(), ".seestar-copy-") {
			rel, err := filepath.Rel(fx.dest, name)
			if err != nil {
				return err
			}
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// checkNoTemp checks that no temporary file is left.
func (fx fixture) checkNoTemp(t *testing.T) {
	t.Helper()
	for _, name := range fx.temps(t) {
		t.Errorf("temporary file left: %s", name)
	}
}

// checkFailed checks that the one result failed with an error containing
// want, and that nothing was recorded or left.
func (fx fixture) checkFailed(t *testing.T, results []Result, want string) {
	t.Helper()
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.Err == nil || !strings.Contains(r.Err.Error(), want) {
		t.Errorf("%s: got err %v, want one containing %q", r.Decision.Path, r.Err, want)
	}
	if r.Record != (state.Record{}) {
		t.Errorf("%s: got record %+v with an error, want the zero record", r.Decision.Path, r.Record)
	}
	if _, ok := fx.st.Lookup(r.Decision.Path); ok {
		t.Errorf("%s is in the state after a failure", r.Decision.Path)
	}
	fx.checkNoTemp(t)
}

func TestCopyWritesFileThenRecord(t *testing.T) {
	fx := newFixture(t, nil)
	c := fx.copier(t)
	sess := device(map[string]string{"MyWorks/M 31/Light_1.fit": "abc", "Autorun/Dark/empty.fit": ""})
	results := run(c, t.Context(), sess, 1, []plan.Decision{
		decision(plan.Copy, "MyWorks/M 31/Light_1.fit", 3),
		decision(plan.Copy, "Autorun/Dark/empty.fit", 0),
	})
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Decision.Path, r.Err)
		}
	}
	fx.checkFile(t, "MyWorks/M 31/Light_1.fit", "abc")
	fx.checkFile(t, "Autorun/Dark/empty.fit", "")
	fx.checkNoTemp(t)
	want := []string{
		`{"path":"MyWorks/M 31/Light_1.fit","size":3,"mtime":"2026-10-29T03:31:19Z","sha256":"` + sumABC + `","at":"2026-11-02T01:02:03Z","how":"copied"}` + "\n",
		`{"path":"Autorun/Dark/empty.fit","size":0,"mtime":"2026-10-29T03:31:19Z","sha256":"` + sumEmpty + `","at":"2026-11-02T01:02:03Z","how":"copied"}` + "\n",
	}
	if got := fx.records(t); !slices.Equal(got, want) {
		t.Errorf("state records:\ngot  %q\nwant %q", got, want)
	}
	wantRec := state.Record{Path: "MyWorks/M 31/Light_1.fit", Size: 3, MTime: deviceMTime, SHA256: sumABC, At: recordedAt, How: state.Copied}
	if len(results) != 2 || results[0].Record != wantRec {
		t.Errorf("first result: got %+v, want record %+v", results, wantRec)
	}
}

// A local file in the way is overwritten by a Copy (Overwrite) and by a
// Replace; a Replace whose local file has gone is copied anew.
func TestCopyRenamesOverLocalFile(t *testing.T) {
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	fx := newFixture(t, func(dest string) {
		writeFile(t, filepath.Join(dest, "MyWorks", "over.fit"), "old local")
		writeFile(t, filepath.Join(dest, "MyWorks", "replace.fit"), "old")
	})
	for _, p := range []string{"MyWorks/replace.fit", "MyWorks/gone.fit"} {
		if err := fx.st.Append(state.Record{Path: p, Size: 3, MTime: old, SHA256: sumEmpty, At: old, How: state.Copied}); err != nil {
			t.Fatal(err)
		}
	}
	c := fx.copier(t)
	sess := device(map[string]string{"MyWorks/over.fit": "abc", "MyWorks/replace.fit": "abc", "MyWorks/gone.fit": "abc"})
	over := decision(plan.Copy, "MyWorks/over.fit", 3)
	over.Overwrite = true
	results := run(c, t.Context(), sess, 1, []plan.Decision{
		over,
		decision(plan.Replace, "MyWorks/replace.fit", 3),
		decision(plan.Replace, "MyWorks/gone.fit", 3),
	})
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Decision.Path, r.Err)
		}
	}
	for _, p := range []string{"MyWorks/over.fit", "MyWorks/replace.fit", "MyWorks/gone.fit"} {
		fx.checkFile(t, p, "abc")
	}
	fx.checkNoTemp(t)
	got := fx.records(t)
	tail := func(p, how string) string {
		return `{"path":"` + p + `","size":3,"mtime":"2026-10-29T03:31:19Z","sha256":"` + sumABC + `","at":"2026-11-02T01:02:03Z","how":"` + how + `"}` + "\n"
	}
	want := []string{tail("MyWorks/over.fit", "copied"), tail("MyWorks/replace.fit", "replaced"), tail("MyWorks/gone.fit", "replaced")}
	if len(got) != 5 || !slices.Equal(got[2:], want) {
		t.Errorf("state records after the two old ones:\ngot  %q\nwant %q", got, want)
	}
}

func TestCopyFailures(t *testing.T) {
	errRead := errors.New("injected read error")
	errOpen := errors.New("injected open error")
	for _, tc := range []struct {
		name  string
		size  int64 // listed; the device file holds "abc"
		fault remote.Fault
		want  string
		is    error
	}{
		{name: "short read", size: 3, fault: remote.Fault{After: 1, ReadErr: io.EOF}, want: "download: short read: got 1 of 3 bytes"},
		{name: "smaller than listed", size: 4, want: "download: short read: got 3 of 4 bytes"},
		{name: "longer than listed", size: 2, want: "download: stopped after 0 of 2 bytes", is: errTooLong},
		{name: "read error", size: 3, fault: remote.Fault{After: 2, ReadErr: errRead}, want: "download: stopped after 2 of 3 bytes", is: errRead},
		{name: "open error", size: 3, fault: remote.Fault{OpenErr: errOpen}, want: "open on the device", is: errOpen},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t, nil)
			c := fx.copier(t)
			sess := device(map[string]string{"MyWorks/a.fit": "abc"})
			sess.Faults = map[string]remote.Fault{"MyWorks/a.fit": tc.fault}
			results := run(c, t.Context(), sess, 1, []plan.Decision{decision(plan.Copy, "MyWorks/a.fit", tc.size)})
			fx.checkFailed(t, results, tc.want)
			if tc.is != nil && !errors.Is(results[0].Err, tc.is) {
				t.Errorf("got err %v, want one wrapping %v", results[0].Err, tc.is)
			}
			fx.checkAbsent(t, "MyWorks/a.fit")
			if got := fx.records(t); len(got) != 0 {
				t.Errorf("state records after a failure: %q", got)
			}
		})
	}
}

// A temporary file left by a failed cleanup after New ran is removed before
// the download creates its own; if it is a symbolic link, what it points to
// is left alone.
func TestCopyRemovesOldTempFile(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "target"), "outside")
	fx := newFixture(t, nil)
	c := fx.copier(t)
	writeFile(t, filepath.Join(fx.dest, "MyWorks", plan.TempName("MyWorks/a.fit")), "partial")
	symlink(t, filepath.Join(outside, "target"), filepath.Join(fx.dest, "MyWorks", plan.TempName("MyWorks/b.fit")))
	results := run(c, t.Context(), device(map[string]string{"MyWorks/a.fit": "abc", "MyWorks/b.fit": "abc"}), 1, []plan.Decision{
		decision(plan.Copy, "MyWorks/a.fit", 3),
		decision(plan.Copy, "MyWorks/b.fit", 3),
	})
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s: %v", r.Decision.Path, r.Err)
		}
	}
	fx.checkFile(t, "MyWorks/a.fit", "abc")
	fx.checkFile(t, "MyWorks/b.fit", "abc")
	fx.checkNoTemp(t)
	if got, err := os.ReadFile(filepath.Join(outside, "target")); err != nil || string(got) != "outside" {
		t.Errorf("file behind the link: got %q, %v; want %q", got, err, "outside")
	}
}

// A failed file does not stop the next one.
func TestCopyContinuesAfterFailure(t *testing.T) {
	fx := newFixture(t, nil)
	c := fx.copier(t)
	sess := device(map[string]string{"MyWorks/a.fit": "abc", "MyWorks/b.fit": "abc"})
	sess.Faults = map[string]remote.Fault{"MyWorks/a.fit": {After: 1, ReadErr: io.EOF}}
	results := run(c, t.Context(), sess, 1, []plan.Decision{
		decision(plan.Copy, "MyWorks/a.fit", 3),
		decision(plan.Copy, "MyWorks/b.fit", 3),
	})
	if len(results) != 2 || results[0].Err == nil || results[1].Err != nil {
		t.Fatalf("got results %+v, want a.fit failed and b.fit copied", results)
	}
	fx.checkAbsent(t, "MyWorks/a.fit")
	fx.checkFile(t, "MyWorks/b.fit", "abc")
}

// The state record comes only after the rename: a rename that fails
// records nothing and leaves no temporary file.
func TestCopyRenameFailureRecordsNothing(t *testing.T) {
	fx := newFixture(t, func(dest string) {
		writeFile(t, filepath.Join(dest, "MyWorks", "a.fit", "inside"), "a folder in the way")
	})
	c := fx.copier(t)
	results := run(c, t.Context(), device(map[string]string{"MyWorks/a.fit": "abc"}), 1,
		[]plan.Decision{decision(plan.Replace, "MyWorks/a.fit", 3)})
	fx.checkFailed(t, results, "rename into place")
	if got := fx.records(t); len(got) != 0 {
		t.Errorf("state records after a failed rename: %q", got)
	}
}

// A record that cannot be appended fails the file, though the file itself is
// complete in place.
func TestCopyStateFailure(t *testing.T) {
	fx := newFixture(t, nil)
	c := fx.copier(t)
	fx.st.Close()
	results := run(c, t.Context(), device(map[string]string{"MyWorks/a.fit": "abc"}), 1,
		[]plan.Decision{decision(plan.Copy, "MyWorks/a.fit", 3)})
	fx.checkFailed(t, results, "record in the state")
	fx.checkFile(t, "MyWorks/a.fit", "abc")
}

func TestAdopt(t *testing.T) {
	fx := newFixture(t, func(dest string) {
		name := filepath.Join(dest, "MyWorks", "M 31", "a.fit")
		writeFile(t, name, "abc")
		if err := os.Chtimes(name, time.Time{}, deviceMTime); err != nil {
			t.Fatal(err)
		}
	})
	c := fx.copier(t)
	// Opening the device file would fail: an adoption downloads nothing.
	sess := device(map[string]string{"MyWorks/M 31/a.fit": "xyz"})
	sess.Faults = map[string]remote.Fault{"MyWorks/M 31/a.fit": {OpenErr: errors.New("adopt opened the device file")}}
	results := run(c, t.Context(), sess, 1, []plan.Decision{decision(plan.Adopt, "MyWorks/M 31/a.fit", 3)})
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("got results %+v, want one success", results)
	}
	fx.checkFile(t, "MyWorks/M 31/a.fit", "abc")
	want := []string{`{"path":"MyWorks/M 31/a.fit","size":3,"mtime":"2026-10-29T03:31:19Z","sha256":"` + sumABC + `","at":"2026-11-02T01:02:03Z","how":"adopted"}` + "\n"}
	if got := fx.records(t); !slices.Equal(got, want) {
		t.Errorf("state records:\ngot  %q\nwant %q", got, want)
	}
}

// A local file whose mtime is a second off the device's, as on a FAT32
// drive, is adopted, and its record keeps the device's mtime.
func TestAdoptMTimeSlop(t *testing.T) {
	const p = "MyWorks/a.fit"
	fx := newFixture(t, func(dest string) {
		writeLocal(t, dest, p, "abc")
		if err := os.Chtimes(filepath.Join(dest, filepath.FromSlash(p)), time.Time{}, deviceMTime.Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
	})
	c := fx.copier(t)
	results := run(c, t.Context(), device(nil), 1, []plan.Decision{decision(plan.Adopt, p, 3)})
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("got results %+v, want one success", results)
	}
	want := []string{`{"path":"MyWorks/a.fit","size":3,"mtime":"2026-10-29T03:31:19Z","sha256":"` + sumABC + `","at":"2026-11-02T01:02:03Z","how":"adopted"}` + "\n"}
	if got := fx.records(t); !slices.Equal(got, want) {
		t.Errorf("state records:\ngot  %q\nwant %q", got, want)
	}
}

// A read-only local file is adopted: an adoption only reads it.
func TestAdoptReadOnly(t *testing.T) {
	const p = "MyWorks/a.fit"
	fx := newFixture(t, func(dest string) {
		writeLocal(t, dest, p, "abc")
		if err := os.Chmod(filepath.Join(dest, filepath.FromSlash(p)), 0o444); err != nil {
			t.Fatal(err)
		}
	})
	c := fx.copier(t)
	results := run(c, t.Context(), device(nil), 1, []plan.Decision{decision(plan.Adopt, p, 3)})
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("got results %+v, want one success", results)
	}
	if _, ok := fx.st.Lookup(p); !ok {
		t.Errorf("%s is not recorded", p)
	}
}

// A local file that changed between the stat and the adoption is not
// adopted.
func TestAdoptChanged(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		mtime      time.Time
		want       string
	}{
		{name: "size", data: "abcd", mtime: deviceMTime, want: "the local file changed: it has size 4"},
		{name: "mtime", data: "abc", mtime: deviceMTime.Add(3 * time.Second), want: "and mtime 2026-10-29T03:31:22Z, not 3 and 2026-10-29T03:31:19Z"},
		{name: "folder", want: "no longer a regular file"},
		{name: "missing", want: "open the local file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t, func(dest string) {
				name := filepath.Join(dest, "MyWorks", "a.fit")
				switch tc.name {
				case "folder":
					if err := os.MkdirAll(name, 0o777); err != nil {
						t.Fatal(err)
					}
				case "missing":
				default:
					writeFile(t, name, tc.data)
					if err := os.Chtimes(name, time.Time{}, tc.mtime); err != nil {
						t.Fatal(err)
					}
				}
			})
			c := fx.copier(t)
			results := run(c, t.Context(), device(nil), 1, []plan.Decision{decision(plan.Adopt, "MyWorks/a.fit", 3)})
			fx.checkFailed(t, results, tc.want)
		})
	}
}

// A symbolic link in the destination that leads outside it cannot redirect
// a download, its new folder, or an adoption.
func TestSymlinkedParentCannotRedirect(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "adopt.fit"), "abc")
	if err := os.Chtimes(filepath.Join(outside, "adopt.fit"), time.Time{}, deviceMTime); err != nil {
		t.Fatal(err)
	}
	fx := newFixture(t, func(dest string) {
		symlink(t, outside, filepath.Join(dest, "MyWorks"))
	})
	c := fx.copier(t)
	sess := device(map[string]string{"MyWorks/a.fit": "abc", "MyWorks/new/b.fit": "abc"})
	for _, d := range []plan.Decision{
		decision(plan.Copy, "MyWorks/a.fit", 3),
		decision(plan.Copy, "MyWorks/new/b.fit", 3),
		decision(plan.Adopt, "MyWorks/adopt.fit", 3),
	} {
		results := run(c, t.Context(), sess, 1, []plan.Decision{d})
		fx.checkFailed(t, results, "escapes")
	}
	des, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(des) != 1 || des[0].Name() != "adopt.fit" {
		t.Errorf("outside the destination: got %v, want only adopt.fit", des)
	}
	if got := fx.records(t); len(got) != 0 {
		t.Errorf("state records: %q", got)
	}
}

// Run leaves out every decision that is not Copy, Replace or Adopt, and
// opens nothing for them.
func TestRunSkipsOtherKinds(t *testing.T) {
	fx := newFixture(t, nil)
	c := fx.copier(t)
	sess := device(map[string]string{"a": "abc"})
	sess.Faults = map[string]remote.Fault{"a": {OpenErr: errors.New("opened")}}
	ds := []plan.Decision{decision(plan.Wait, "a", 3), decision(plan.Done, "a", 3), decision(plan.Fail, "a", 3)}
	if results := run(c, context.Background(), sess, 4, ds); len(results) != 0 {
		t.Errorf("got results %+v, want none", results)
	}
	if got := fx.records(t); len(got) != 0 {
		t.Errorf("state records: %q", got)
	}
}

func TestHashExactly(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		size    int64
		want    string // the sum, or what the error contains
		wantErr error
	}{
		{name: "exact", ctx: t.Context(), size: 3, want: sumABC},
		{name: "short", ctx: t.Context(), size: 4, want: "short read: got 3 of 4 bytes"},
		{name: "long", ctx: t.Context(), size: 2, want: "stopped after 0 of 2 bytes", wantErr: errTooLong},
		{name: "canceled", ctx: canceled, size: 3, want: "stopped after 0 of 3 bytes", wantErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w strings.Builder
			sum, err := hashExactly(tc.ctx, &w, strings.NewReader("abc"), tc.size)
			if tc.want == sumABC {
				if err != nil || sum != sumABC || w.String() != "abc" {
					t.Errorf("got %q, %v, wrote %q; want %s, nil, wrote %q", sum, err, w.String(), sumABC, "abc")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || sum != "" {
				t.Errorf("got %q, %v; want an error containing %q", sum, err, tc.want)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("got err %v, want one wrapping %v", err, tc.wantErr)
			}
		})
	}
}

// The device mtime's nanoseconds reach both the local file and the state,
// and a file longer than one read buffer hashes whole.
func TestCopyFractionalMTime(t *testing.T) {
	const p = "MyWorks/a.fit"
	fx := newFixture(t, nil)
	c := fx.copier(t)
	// Windows and SMB keep times in units of 100 ns.
	mtime := time.Date(2026, 10, 29, 3, 31, 19, 123456700, time.UTC)
	body := strings.Repeat("a", 65539)
	d := decision(plan.Copy, p, int64(len(body)))
	d.ModTime = mtime
	results := run(c, t.Context(), device(map[string]string{p: body}), 1, []plan.Decision{d})
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("got results %+v, want one success", results)
	}
	// The SHA-256 of 65539 bytes of "a", computed independently.
	const sum = "2063eabf1a5e48a6a7569d05cbe722c522a6d050b1d07c5e70c906a5d0fceb18"
	want := []string{`{"path":"MyWorks/a.fit","size":65539,"mtime":"2026-10-29T03:31:19.1234567Z","sha256":"` + sum + `","at":"2026-11-02T01:02:03Z","how":"copied"}` + "\n"}
	if got := fx.records(t); !slices.Equal(got, want) {
		t.Errorf("state records:\ngot  %q\nwant %q", got, want)
	}
	fi, err := os.Stat(filepath.Join(fx.dest, filepath.FromSlash(p)))
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(mtime) {
		t.Errorf("local mtime %s, want %s", fi.ModTime().UTC().Format(time.RFC3339Nano), mtime.Format(time.RFC3339Nano))
	}
}

// trackedSession counts the device files opened and closed.
type trackedSession struct {
	remote.Session
	opened, closed atomic.Int32
}

type trackedFile struct {
	remote.File
	s *trackedSession
}

func (s *trackedSession) Open(ctx context.Context, p string) (remote.File, error) {
	f, err := s.Session.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	s.opened.Add(1)
	return &trackedFile{File: f, s: s}, nil
}

func (f *trackedFile) Close() error {
	f.s.closed.Add(1)
	return f.File.Close()
}

// The device file is closed whether the download succeeds, fails or is
// canceled.
func TestCopyClosesDeviceFile(t *testing.T) {
	for _, mode := range []string{"success", "read error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			fx := newFixture(t, nil)
			c := fx.copier(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fake := device(map[string]string{"MyWorks/a.fit": "abc"})
			switch mode {
			case "read error":
				fake.Faults = map[string]remote.Fault{"MyWorks/a.fit": {After: 1, ReadErr: errors.New("injected read error")}}
			case "cancel":
				fake.Faults = map[string]remote.Fault{"MyWorks/a.fit": {After: 1, Stall: cancel}}
			}
			sess := &trackedSession{Session: fake}
			results := run(c, ctx, sess, 1, []plan.Decision{decision(plan.Copy, "MyWorks/a.fit", 3)})
			if len(results) != 1 || (results[0].Err == nil) != (mode == "success") {
				t.Fatalf("got results %+v", results)
			}
			if opened, closed := sess.opened.Load(), sess.closed.Load(); opened != 1 || closed != 1 {
				t.Errorf("device files opened %d and closed %d, want 1 and 1", opened, closed)
			}
		})
	}
}
