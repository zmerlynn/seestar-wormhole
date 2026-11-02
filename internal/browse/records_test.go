package browse

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/state"
)

// counter is a load function that counts its calls and returns a new map
// each time, holding the call number, so a test can tell which load a result
// came from.
type counter struct {
	n   int
	err error
}

func (c *counter) load() (map[string]state.Record, error) {
	c.n++
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

func TestCacheRecordsReusedWhenUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, path, "a\n", t0)
	c := &counter{}
	records := CacheRecords(path, c.load)
	get(t, records, 1)
	get(t, records, 1)
	get(t, records, 1)
	if c.n != 1 {
		t.Errorf("load called %d times, want 1", c.n)
	}
}

func TestCacheRecordsReloadsAfterAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, path, "a\n", t0)
	c := &counter{}
	records := CacheRecords(path, c.load)
	get(t, records, 1)
	// The same mtime, as on a filesystem whose mtimes are too coarse to
	// see the append: only the size changes.
	writeFile(t, path, "a\nb\n", t0)
	get(t, records, 2)
	get(t, records, 2)
}

func TestCacheRecordsReloadsAfterSameSizeRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, path, "a\n", t0)
	c := &counter{}
	records := CacheRecords(path, c.load)
	get(t, records, 1)
	writeFile(t, path, "b\n", t0.Add(time.Second))
	get(t, records, 2)
	get(t, records, 2)
}

func TestCacheRecordsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	c := &counter{}
	records := CacheRecords(path, c.load)
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
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, path, "a\n", t0)
	boom := errors.New("boom")
	c := &counter{err: boom}
	records := CacheRecords(path, c.load)
	if _, err := records(); !errors.Is(err, boom) {
		t.Fatalf("records: %v, want %v", err, boom)
	}
	c.err = nil
	get(t, records, 2)
	get(t, records, 2)
}
