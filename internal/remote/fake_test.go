package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"
)

func sameEntries(t *testing.T, got, want []Entry) {
	t.Helper()
	equal := slices.EqualFunc(got, want, func(a, b Entry) bool {
		return a.Path == b.Path && a.Size == b.Size && a.Kind == b.Kind && a.ModTime.Equal(b.ModTime)
	})
	if !equal {
		var b strings.Builder
		for _, e := range got {
			fmt.Fprintf(&b, "  %v %d %d %q\n", e.Kind, e.Size, e.ModTime.Unix(), e.Path)
		}
		t.Errorf("listing differs from the fixture; got:\n%s", b.String())
	}
}

func TestWalkKinds(t *testing.T) {
	mtime := time.Unix(1761000000, 0)
	fsys := fstest.MapFS{
		"dir":  {Mode: fs.ModeDir | 0o755, ModTime: mtime},
		"file": {Data: []byte("abc"), ModTime: mtime},
		"link": {Data: []byte("file"), Mode: fs.ModeSymlink | 0o777, ModTime: mtime},
		"pipe": {Mode: fs.ModeNamedPipe, ModTime: mtime},
		"dev":  {Mode: fs.ModeDevice, ModTime: mtime},
	}
	got, err := NewFake(fsys).Walk(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{Path: "dev", Kind: Other, ModTime: mtime},
		{Path: "dir", Kind: Dir, ModTime: mtime},
		{Path: "file", Size: 3, Kind: Regular, ModTime: mtime},
		{Path: "link", Size: 4, Kind: Other, ModTime: mtime},
		{Path: "pipe", Kind: Other, ModTime: mtime},
	}
	sameEntries(t, got, want)
	for k, s := range map[Kind]string{Other: "other", Regular: "regular", Dir: "dir", Kind(9): "other"} {
		if k.String() != s {
			t.Errorf("Kind(%d).String() = %q, want %q", k, k.String(), s)
		}
	}
}

// renamingFS lists the last entry of one directory under a different name,
// the way a hostile or broken server could.
type renamingFS struct {
	fstest.MapFS
	dir, name string
}

type renamedEntry struct {
	fs.DirEntry
	name string
}

func (e renamedEntry) Name() string { return e.name }

func (r renamingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := r.MapFS.ReadDir(name)
	if name == r.dir && err == nil && len(entries) > 0 {
		last := len(entries) - 1
		entries[last] = renamedEntry{entries[last], r.name}
	}
	return entries, err
}

func TestWalkRefusesNamesThatAreNotOneElement(t *testing.T) {
	base := fstest.MapFS{"a/b": {Data: []byte("x")}, "c": {Data: []byte("y")}}
	for _, name := range []string{"", ".", "..", "x/y", "../c"} {
		for _, dir := range []string{".", "a"} {
			_, err := NewFake(renamingFS{base, dir, name}).Walk(t.Context(), nil, nil)
			if err == nil {
				t.Errorf("listing %q in %s: err = %v, want a refusal", name, dir, err)
			}
		}
	}
	// Names that only look odd are kept byte for byte.
	for _, name := range []string{"...", ".x", "a\\b", "a b ", "\xff"} {
		got, err := NewFake(renamingFS{base, ".", name}).Walk(t.Context(), nil, nil)
		if err != nil {
			t.Errorf("listing %q: %v", name, err)
			continue
		}
		if last := got[len(got)-1]; last.Path != name || last.Kind != Regular {
			t.Errorf("listing %q gave %+v", name, last)
		}
	}
}

// failingFS fails ReadDir of one directory.
type failingFS struct {
	fstest.MapFS
	dir string
	err error
}

func (f failingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == f.dir {
		return nil, f.err
	}
	return f.MapFS.ReadDir(name)
}

func TestWalkErrors(t *testing.T) {
	// Failures come back as they are, with no partial listing.
	base := fstest.MapFS{"a/b": {Data: []byte("x")}, "c": {Data: []byte("y")}}
	boom := errors.New("boom")
	for _, tc := range []struct {
		dir string
		err error
	}{{"a", fs.ErrNotExist}, {"a", boom}, {".", fs.ErrPermission}} {
		got, err := NewFake(failingFS{base, tc.dir, tc.err}).Walk(t.Context(), nil, nil)
		if !errors.Is(err, tc.err) || got != nil {
			t.Errorf("ReadDir(%q) failing with %v: Walk = %v, %v", tc.dir, tc.err, got, err)
		}
	}
}

// readsFS records every ReadDir and fails the one of dir.
type readsFS struct {
	fstest.MapFS
	dir   string
	reads *[]string
}

func (r readsFS) ReadDir(name string) ([]fs.DirEntry, error) {
	*r.reads = append(*r.reads, name)
	if name == r.dir {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrPermission}
	}
	return r.MapFS.ReadDir(name)
}

// A skipped folder is never read, so one that cannot be read does not fail
// the walk; the same folder not skipped does. The root is never offered to
// skip, so a skip that matches everything gives an empty listing.
func TestWalkSkip(t *testing.T) {
	base := fstest.MapFS{
		".hidden/x": {Data: []byte("x")},
		"a/x":       {Data: []byte("x")},
		"a/y":       {Data: []byte("y")},
		"bad/z":     {Data: []byte("z")},
		"c":         {Data: []byte("c")},
	}
	var reads []string
	skip := map[string]bool{".hidden": true, "a/x": true, "bad": true}
	var asked []string
	got, err := NewFake(readsFS{base, "bad", &reads}).Walk(t.Context(), func(p string, dir bool) bool {
		if dir {
			p += "/"
		}
		asked = append(asked, p)
		return skip[strings.TrimSuffix(p, "/")]
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{".hidden/", "a/", "a/x", "a/y", "bad/", "c"}; !slices.Equal(asked, want) {
		t.Errorf("skip asked about %q, want %q", asked, want)
	}
	if want := []string{".", "a"}; !slices.Equal(reads, want) {
		t.Errorf("ReadDir calls %q, want %q", reads, want)
	}
	var paths []string
	for _, e := range got {
		paths = append(paths, e.Path)
	}
	if want := []string{"a", "a/y", "c"}; !slices.Equal(paths, want) {
		t.Errorf("listing %q, want %q", paths, want)
	}

	reads = nil
	got, err = NewFake(readsFS{base, "bad", &reads}).Walk(t.Context(), func(p string, _ bool) bool { return p != "bad" }, nil)
	if !errors.Is(err, fs.ErrPermission) || got != nil {
		t.Errorf("unreadable folder not skipped: Walk = %v, %v; want the error and no listing", got, err)
	}

	got, err = NewFake(base).Walk(t.Context(), func(string, bool) bool { return true }, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("skipping every entry: Walk = %v, %v; want an empty listing", got, err)
	}
}

// cancelingFS cancels a context when one directory is listed.
type cancelingFS struct {
	fstest.MapFS
	dir    string
	cancel context.CancelFunc
}

func (c cancelingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == c.dir {
		c.cancel()
	}
	return c.MapFS.ReadDir(name)
}

// Walk's kept func gets each entry kept, with its size and mtime, as it
// is listed: before the walk reads the next folder, and also when a later
// folder fails the walk. Skipped entries never reach it.
func TestWalkListed(t *testing.T) {
	mtime := time.Unix(1761000000, 0)
	base := fstest.MapFS{
		"a/x":   {Data: []byte("xx"), ModTime: mtime},
		"a/y":   {Data: []byte("y"), ModTime: mtime},
		"b/z":   {Data: []byte("z"), ModTime: mtime},
		"bad/z": {Data: []byte("z"), ModTime: mtime},
	}
	var log []string
	var listed []Entry
	kept := func(e Entry) {
		listed = append(listed, e)
		log = append(log, "listed "+e.Path)
	}
	got, err := NewFake(readsFS{base, "bad", &log}).Walk(t.Context(), func(p string, _ bool) bool { return p == "a/y" || p == "bad" }, kept)
	if err != nil {
		t.Fatal(err)
	}
	sameEntries(t, listed, got)
	if want := []string{".", "listed a", "a", "listed a/x", "listed b", "b", "listed b/z"}; !slices.Equal(log, want) {
		t.Errorf("calls %q, want %q", log, want)
	}
	if len(listed) != 4 || listed[1].Size != 2 || !listed[1].ModTime.Equal(mtime) {
		t.Errorf("listed %v, want a/x with size 2 and its mtime", listed)
	}

	listed = nil
	got, err = NewFake(readsFS{base, "bad", &log}).Walk(t.Context(), nil, kept)
	if !errors.Is(err, fs.ErrPermission) || got != nil {
		t.Fatalf("Walk = %v, %v; want the error and no listing", got, err)
	}
	var paths []string
	for _, e := range listed {
		paths = append(paths, e.Path)
	}
	if want := []string{"a", "a/x", "a/y", "b", "b/z", "bad"}; !slices.Equal(paths, want) {
		t.Errorf("listed before the failure %q, want %q", paths, want)
	}
}

func TestWalkStopsWhenCanceledMidway(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fsys := cancelingFS{fstest.MapFS{"a/b": {Data: []byte("x")}, "c": {Data: []byte("y")}}, "a", cancel}
	got, err := NewFake(fsys).Walk(ctx, nil, nil)
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Errorf("Walk = %v, %v; want context.Canceled and no listing", got, err)
	}
}

func TestFakeFaults(t *testing.T) {
	fsys := fstest.MapFS{"f": {Data: bytes.Repeat([]byte("0123456789"), 10000)}}
	boom := errors.New("boom")

	t.Run("OpenErr", func(t *testing.T) {
		fake := NewFake(fsys)
		fake.Faults = map[string]Fault{"f": {OpenErr: boom}}
		if _, err := fake.Open(t.Context(), "f"); !errors.Is(err, boom) {
			t.Errorf("Open = %v", err)
		}
	})

	t.Run("ReadErr", func(t *testing.T) {
		fake := NewFake(fsys)
		fake.Faults = map[string]Fault{"f": {After: 40000, ReadErr: boom}}
		f, err := fake.Open(t.Context(), "f")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		n, err := io.Copy(io.Discard, f)
		if n != 40000 || !errors.Is(err, boom) {
			t.Errorf("copy = %d, %v; want 40000, boom", n, err)
		}
	})

	t.Run("ShortFile", func(t *testing.T) {
		fake := NewFake(fsys)
		fake.Faults = map[string]Fault{"f": {After: 123, ReadErr: io.EOF}}
		f, err := fake.Open(t.Context(), "f")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		n, err := io.Copy(io.Discard, f)
		if n != 123 || err != nil {
			t.Errorf("copy = %d, %v; want 123, nil", n, err)
		}
	})

	t.Run("Stall", func(t *testing.T) {
		fake := NewFake(fsys)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		fake.Faults = map[string]Fault{"f": {After: 5000, Stall: cancel}}
		f, err := fake.Open(ctx, "f")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		n, err := io.Copy(io.Discard, f)
		if n != 5000 || !errors.Is(err, context.Canceled) {
			t.Errorf("copy = %d, %v; want 5000, context.Canceled", n, err)
		}
	})

	t.Run("OtherPathsUnaffected", func(t *testing.T) {
		fake := NewFake(fstest.MapFS{"f": {Data: []byte("abc")}, "g": {Data: []byte("defg")}})
		fake.Faults = map[string]Fault{"f": {OpenErr: boom}, "g": {After: 0}}
		f, err := fake.Open(t.Context(), "g")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if b, err := io.ReadAll(f); string(b) != "defg" || err != nil {
			t.Errorf("ReadAll = %q, %v", b, err)
		}
	})

	t.Run("CanceledOpen", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := NewFake(fsys).Open(ctx, "f"); !errors.Is(err, context.Canceled) {
			t.Errorf("Open = %v", err)
		}
	})
}

// countingFS counts ReadDir calls, renames the first entry of the root, and
// refuses to list more than max directories so a walk that loops fails
// instead of running out of memory.
type countingFS struct {
	fstest.MapFS
	name  string
	max   int
	reads int
}

var errTooManyReadDirs = errors.New("too many ReadDir calls")

func (c *countingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	c.reads++
	if c.reads > c.max {
		return nil, errTooManyReadDirs
	}
	entries, err := c.MapFS.ReadDir(name)
	if name == "." && err == nil && len(entries) > 0 {
		entries[0] = renamedEntry{entries[0], c.name}
	}
	return entries, err
}

func TestWalkRefusesDirectoryAliases(t *testing.T) {
	// A directory listed as "" or "." joins back to the root itself, and
	// one listed as ".." or "x/y" to somewhere else; each must be refused
	// before its contents are listed.
	for _, name := range []string{"", ".", "..", "x/y"} {
		fsys := &countingFS{MapFS: fstest.MapFS{"a/file": {Data: []byte("x")}}, name: name, max: 5}
		got, err := NewFake(fsys).Walk(t.Context(), nil, nil)
		if err == nil || errors.Is(err, errTooManyReadDirs) || got != nil {
			t.Errorf("directory listed as %q: Walk = %v, %v; want a refusal", name, got, err)
		}
		if fsys.reads != 1 {
			t.Errorf("directory listed as %q: %d ReadDir calls, want only the root's", name, fsys.reads)
		}
	}
}

// statErrorFS fails Stat of one path.
type statErrorFS struct {
	fstest.MapFS
	path string
	err  error
}

func (s statErrorFS) Stat(name string) (fs.FileInfo, error) {
	if name == s.path {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: s.err}
	}
	return s.MapFS.Stat(name)
}

func TestWalkRootErrors(t *testing.T) {
	base := fstest.MapFS{"a": {Data: []byte("x")}}
	for _, want := range []error{fs.ErrNotExist, fs.ErrPermission, errors.New("i/o error")} {
		got, err := NewFake(statErrorFS{base, ".", want}).Walk(t.Context(), nil, nil)
		if !errors.Is(err, want) || got != nil {
			t.Errorf("root Stat failing with %v: Walk = %v, %v; want that error and no listing", want, got, err)
		}
	}
	got, err := NewFake(fstest.MapFS{".": {Data: []byte("x")}}).Walk(t.Context(), nil, nil)
	if err == nil || got != nil {
		t.Errorf("root that is a file: Walk = %v, %v; want a refusal", got, err)
	}
}

func TestFakeCloseEndsStalledRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := NewFake(fstest.MapFS{"f": {Data: []byte("hello")}})
		stalled := make(chan struct{})
		fake.Faults = map[string]Fault{"f": {After: 2, Stall: func() { close(stalled) }}}
		// The Open context is never canceled; only Close can end the read.
		f, err := fake.Open(context.Background(), "f")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		done := make(chan error, 1)
		go func() {
			n, err := f.WriteTo(io.Discard)
			if n != 2 {
				t.Errorf("WriteTo wrote %d bytes before the stall, want 2", n)
			}
			done <- err
		}()
		<-stalled
		if err := fake.Close(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, fs.ErrClosed) {
				t.Errorf("stalled WriteTo after Close = %v, want fs.ErrClosed", err)
			}
		default:
			t.Fatal("Close left the stalled WriteTo blocked")
		}
	})
}

// closeCountFS counts Close calls on the files it opens.
type closeCountFS struct {
	fstest.MapFS
	closes atomic.Int32
}

type closeCountFile struct {
	fs.File
	owner *closeCountFS
}

func (c *closeCountFS) Open(name string) (fs.File, error) {
	f, err := c.MapFS.Open(name)
	if err != nil {
		return nil, err
	}
	return closeCountFile{f, c}, nil
}

func (f closeCountFile) Close() error {
	f.owner.closes.Add(1)
	return f.File.Close()
}

func TestFakeFileCloseClosesUnderlyingFile(t *testing.T) {
	fsys := &closeCountFS{MapFS: fstest.MapFS{"f": {Data: []byte("x")}}}
	f, err := NewFake(fsys).Open(t.Context(), "f")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if n := fsys.closes.Load(); n != 1 {
		t.Errorf("underlying file closed %d times, want 1", n)
	}
}
