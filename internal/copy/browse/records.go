package browse

import (
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

// CacheRecords returns a Records function that keeps the last records load
// gave for dest's copy history, keyed by the state file load reads, its
// modification time and its size. Each call asks dir for the history
// folder, which a run that writes may move while the page is open, stats
// dest's state file there, and calls load with that folder again only when
// the file is another one or either has changed: a sync appends to the
// file, and the size catches an append that a coarse mtime would miss. A
// missing file is not cached, so load decides what it means each time, and
// neither is a failed stat or load.
func CacheRecords(dir func() (string, error), dest string, load func(dir string) (map[string]state.Record, error)) func() (map[string]state.Record, error) {
	var (
		mu      sync.Mutex
		cached  bool
		file    string
		mtime   time.Time
		size    int64
		records map[string]state.Record
	)
	return func() (map[string]state.Record, error) {
		mu.Lock()
		defer mu.Unlock()
		d, err := dir()
		if err != nil {
			return nil, err
		}
		f, err := state.Path(d, dest)
		if err != nil {
			return nil, err
		}
		fi, err := os.Stat(f)
		if errors.Is(err, fs.ErrNotExist) {
			cached = false
			return load(d)
		}
		if err != nil {
			return nil, err
		}
		if cached && f == file && fi.ModTime().Equal(mtime) && fi.Size() == size {
			return records, nil
		}
		cached = false
		r, err := load(d)
		if err != nil {
			return nil, err
		}
		cached, file, mtime, size, records = true, f, fi.ModTime(), fi.Size(), r
		return records, nil
	}
}
