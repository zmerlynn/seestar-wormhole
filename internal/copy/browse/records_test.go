package browse

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

// counter is a load function that counts its calls and returns a new map
// each time, holding the call number, so a test can tell which load a result
// came from. It records the folder of its last call.
type counter struct {
	n   int
	err error
	dir string
}

func (c *counter) load(dir string) (map[string]state.Record, error) {
	c.n++
	c.dir = dir
	if c.err != nil {
		return nil, c.err
	}
	return map[string]state.Record{"n": {Size: int64(c.n)}}, nil
}

// get calls records and checks which load its result came from.
func get(t *testing.T, records func() (map[string]state.Record, error), wantLoad int64) {
	t.Helper()
	r, err := records()
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	if got := r["n"].Size; got != wantLoad {
		t.Fatalf("records came from load %d, want load %d", got, wantLoad)
	}
}

func writeFile(t *testing.T, path, data string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// history is a history folder for a destination, as CacheRecords's dir
// gives it, and the path of the destination's state file there, whose
// folder exists.
func history(t *testing.T) (dir func() (string, error), dest, path string) {
	t.Helper()
	d, dest := t.TempDir(), filepath.Join(t.TempDir(), "dest")
	return folder(d), dest, statePath(t, d, dest)
}

// folder is a dir function that gives d.
func folder(d string) func() (string, error) {
	return func() (string, error) { return d, nil }
}

// statePath is dest's state file in the history folder d, whose folder
// exists.
func statePath(t *testing.T, d, dest string) string {
	t.Helper()
	path, err := state.Path(d, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCacheRecordsReusedWhenUnchanged(t *testing.T) {
	dir, dest, path := history(t)
	writeFile(t, path, "a\n", t0)
	c := &counter{}
	records := CacheRecords(dir, dest, c.load)
	get(t, records, 1)
	get(t, records, 1)
	get(t, records, 1)
	if c.n != 1 {
		t.Errorf("load called %d times, want 1", c.n)
	}
}

func TestCacheRecordsReloadsAfterAppend(t *testing.T) {
	dir, dest, path := history(t)
	writeFile(t, path, "a\n", t0)
	c := &counter{}
	records := CacheRecords(dir, dest, c.load)
	get(t, records, 1)
	// The same mtime, as on a filesystem whose mtimes are too coarse to
	// see the append: only the size changes.
	writeFile(t, path, "a\nb\n", t0)
	get(t, records, 2)
	get(t, records, 2)
}

func TestCacheRecordsReloadsAfterSameSizeRewrite(t *testing.T) {
	dir, dest, path := history(t)
	writeFile(t, path, "a\n", t0)
	c := &counter{}
	records := CacheRecords(dir, dest, c.load)
	get(t, records, 1)
	writeFile(t, path, "b\n", t0.Add(time.Second))
	get(t, records, 2)
	get(t, records, 2)
}

func TestCacheRecordsMissingFile(t *testing.T) {
	dir, dest, path := history(t)
	c := &counter{}
	records := CacheRecords(dir, dest, c.load)
	// load decides what a missing file means, each time.
	get(t, records, 1)
	get(t, records, 2)
	writeFile(t, path, "a\n", t0)
	get(t, records, 3)
	get(t, records, 3)
	// A file that goes away is not served from the cache.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	get(t, records, 4)
}

func TestCacheRecordsFailureNotCached(t *testing.T) {
	dir, dest, path := history(t)
	writeFile(t, path, "a\n", t0)
	boom := errors.New("boom")
	c := &counter{err: boom}
	records := CacheRecords(dir, dest, c.load)
	if _, err := records(); !errors.Is(err, boom) {
		t.Fatalf("records: %v, want %v", err, boom)
	}
	c.err = nil
	get(t, records, 2)
	get(t, records, 2)
}

// Each call follows the folder dir gives: the state file in another folder
// is loaded from that folder, even with the same mtime and size, and is then
// kept until it changes. The first folder's file no longer counts.
func TestCacheRecordsFollowsFolder(t *testing.T) {
	first, dest, path := history(t)
	writeFile(t, path, "a\n", t0)
	d := t.TempDir()
	moved := statePath(t, d, dest)
	writeFile(t, moved, "a\n", t0)
	dir := first
	c := &counter{}
	records := CacheRecords(func() (string, error) { return dir() }, dest, c.load)
	get(t, records, 1)
	dir = folder(d)
	get(t, records, 2)
	if c.dir != d {
		t.Errorf("loaded from %s, want %s", c.dir, d)
	}
	get(t, records, 2)
	// The first folder's file changing, as an older installation's sync
	// writes it, is not this history.
	writeFile(t, path, "a\nb\n", t0)
	get(t, records, 2)
	writeFile(t, moved, "a\nb\n", t0)
	get(t, records, 3)
	get(t, records, 3)
}

// A folder dir can't give is an error, and nothing is loaded.
func TestCacheRecordsFolderFailure(t *testing.T) {
	_, dest, _ := history(t)
	boom := errors.New("boom")
	c := &counter{}
	records := CacheRecords(func() (string, error) { return "", boom }, dest, c.load)
	if _, err := records(); !errors.Is(err, boom) || c.n != 0 {
		t.Fatalf("records: %v after %d loads, want %v and none", err, c.n, boom)
	}
}
