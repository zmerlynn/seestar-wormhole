package browse

import (
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/state"
)

// CacheRecords returns a Records function that keeps the last records load
// gave, keyed by the modification time and size of file, the state file load
// reads. Each call stats file and calls load again only when either has
// changed: a sync appends to the file, and the size catches an append that a
// coarse mtime would miss. A missing file is not cached, so load decides what
// it means each time, and neither is a failed stat or load.
func CacheRecords(file string, load func() (map[string]state.Record, error)) func() (map[string]state.Record, error) {
	var (
		mu      sync.Mutex
		cached  bool
		mtime   time.Time
		size    int64
		records map[string]state.Record
	)
	return func() (map[string]state.Record, error) {
		mu.Lock()
		defer mu.Unlock()
		fi, err := os.Stat(file)
		if errors.Is(err, fs.ErrNotExist) {
			cached = false
			return load()
		}
		if err != nil {
			return nil, err
		}
		if cached && fi.ModTime().Equal(mtime) && fi.Size() == size {
			return records, nil
		}
		cached = false
		r, err := load()
		if err != nil {
			return nil, err
		}
		cached, mtime, size, records = true, fi.ModTime(), fi.Size(), r
		return records, nil
	}
}
