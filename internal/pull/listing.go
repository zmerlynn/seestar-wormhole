package pull

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

const listingPageSize = 256
const maxListingBytes = 64 << 20

// listingBudget is shared by all directory readers in one inventory. The mutex
// protects counters only; it is never held across Readdir or backend metadata.
// Entry count bounds metadata overhead; bytes bounds retained source path text.
// go-smb2 may also buffer one server response behind each incremental reader.
type listingBudget struct {
	mu             sync.Mutex
	entries, bytes int
	exceeded       bool
}

func (b *listingBudget) take(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.exceeded || b.entries >= maxEntries || len(name) > maxListingBytes-b.bytes {
		b.exceeded = true
		return failure(diagnostic.InventoryLimit)
	}
	b.entries++
	b.bytes += len(name)
	return nil
}

func (b *listingBudget) readDir(ctx context.Context, dir string, readdir func(int) ([]os.FileInfo, error)) ([]os.FileInfo, error) {
	var entries []os.FileInfo
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b.mu.Lock()
		exceeded := b.exceeded
		b.mu.Unlock()
		if exceeded {
			return nil, failure(diagnostic.InventoryLimit)
		}
		page, err := readdir(listingPageSize)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		for _, info := range page {
			name := info.Name()
			if dir != "" {
				name = dir + "/" + name
			}
			// Validate before rclone joins/cleans the raw SMB name as well as before
			// the walker filters the resulting Fs entries.
			if err := validateListedRemote(dir, name); err != nil {
				return nil, err
			}
			if err := b.take(name); err != nil {
				return nil, err
			}
		}
		entries = append(entries, page...)
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if len(page) == 0 {
			return nil, io.ErrNoProgress
		}
	}
}
