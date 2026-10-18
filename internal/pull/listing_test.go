package pull

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/smb"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/object"
)

type listingInfo struct{ name string }

func (i listingInfo) Name() string     { return i.name }
func (listingInfo) Size() int64        { return 1 }
func (listingInfo) Mode() os.FileMode  { return 0 }
func (listingInfo) ModTime() time.Time { return time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC) }
func (listingInfo) IsDir() bool        { return false }
func (listingInfo) Sys() any           { return nil }

type pagedEnumerator struct {
	total    int
	name     string
	produced atomic.Int64
}

func (p *pagedEnumerator) read(n int) ([]os.FileInfo, error) {
	remaining := p.total - int(p.produced.Load())
	if remaining == 0 {
		return nil, io.EOF
	}
	if n <= 0 || n > remaining {
		n = remaining
	}
	entries := make([]os.FileInfo, n)
	for i := range entries {
		entries[i] = listingInfo{p.name}
	}
	p.produced.Add(int64(n))
	return entries, nil
}

type pagedListingFs struct {
	fs.Fs
	pages map[string]*pagedEnumerator
}

func (f pagedListingFs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	// This is the same dispatcher the patched SMB List calls after Open. There
	// is no Samba fixture: only go-smb2's incremental Readdir contract is modeled.
	infos, err := smb.ReadDir(ctx, dir, f.pages[dir].read)
	if err != nil {
		return nil, err
	}
	entries := make(fs.DirEntries, len(infos))
	for i, info := range infos {
		name := info.Name()
		if dir != "" {
			name = dir + "/" + name
		}
		entries[i] = object.NewMemoryObject(name, info.ModTime(), []byte("x"))
	}
	return entries, nil
}

func TestSMBListingBudgetStopsEnumeration(t *testing.T) {
	for _, test := range []struct {
		name             string
		bytes, aggregate bool
	}{
		{name: "entries"},
		{name: "entries-aggregate", aggregate: true},
		{name: "bytes", bytes: true},
		{name: "bytes-aggregate", bytes: true, aggregate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, d, src, _, now := fixture(t)
			c.DryRun = true
			dirs := []string{""}
			if test.aggregate {
				dirs = []string{"A", "B"}
				c.Dirs = dirs
				d.State.Source = ""
			}
			name := "file.fit"
			limit := maxEntries
			if test.aggregate {
				limit -= len(dirs)
			}
			if test.bytes {
				name = strings.Repeat("n", 2044) + ".fit"
				nameBytes := len(name)
				if test.aggregate {
					nameBytes += 2
				} // directory prefix and slash
				limit = maxListingBytes / nameBytes
			}
			pages := map[string]*pagedEnumerator{}
			for _, dir := range dirs {
				pages[dir] = &pagedEnumerator{total: limit/len(dirs) + 2*listingPageSize, name: name}
			}
			_, err := d.Run(t.Context(), c, now, pagedListingFs{src, pages})
			produced := 0
			for _, p := range pages {
				produced += int(p.produced.Load())
			}
			if produced > limit+listingPageSize {
				t.Fatalf("produced=%d want at most %d (budget=%d plus one page); enumeration consumed oversized listing", produced, limit+listingPageSize, limit)
			}
			requireReason(t, err, "inventory_limit")
			t.Logf("produced=%d budget=%d page=%d", produced, limit, listingPageSize)
		})
	}
}

func TestSMBListingExactBudgets(t *testing.T) {
	for _, exhausted := range []string{"entries", "bytes"} {
		t.Run(exhausted, func(t *testing.T) {
			budget := &listingBudget{}
			name := "file.fit"
			if exhausted == "entries" {
				budget.entries = maxEntries - 2
			} else {
				budget.bytes = maxListingBytes - 2*len(name)
			}
			ctx := smb.WithReadDir(t.Context(), budget.readDir)
			p := &pagedEnumerator{total: 2, name: name}
			infos, err := smb.ReadDir(ctx, "", p.read)
			if err != nil || len(infos) != 2 {
				t.Fatalf("exact budget: entries=%d err=%v", len(infos), err)
			}
			p = &pagedEnumerator{total: 1, name: name}
			_, err = smb.ReadDir(ctx, "", p.read)
			requireReason(t, err, "inventory_limit")
		})
	}
}

func TestSMBListingReadOutcomes(t *testing.T) {
	sentinel := io.ErrUnexpectedEOF
	for _, mode := range []string{"pages", "partial-eof", "read-error", "canceled-before", "canceled-between", "empty-page"} {
		t.Run(mode, func(t *testing.T) {
			budget := &listingBudget{}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx = smb.WithReadDir(ctx, budget.readDir)
			calls := 0
			p := &pagedEnumerator{total: listingPageSize + 1, name: "file.fit"}
			if mode == "canceled-before" {
				cancel()
			}
			infos, err := smb.ReadDir(ctx, "", func(n int) ([]os.FileInfo, error) {
				calls++
				if n <= 0 || n > listingPageSize {
					t.Fatalf("Readdir(%d) must be incremental", n)
				}
				switch mode {
				case "partial-eof":
					return []os.FileInfo{listingInfo{"file.fit"}}, io.EOF
				case "read-error":
					return nil, sentinel
				case "canceled-between":
					cancel()
					return []os.FileInfo{listingInfo{"file.fit"}}, nil
				case "empty-page":
					return nil, nil
				default:
					return p.read(n)
				}
			})
			switch mode {
			case "pages":
				if err != nil || len(infos) != listingPageSize+1 || calls != 3 {
					t.Fatalf("entries=%d calls=%d err=%v", len(infos), calls, err)
				}
			case "partial-eof":
				if err != nil || len(infos) != 1 {
					t.Fatalf("partial EOF: entries=%d err=%v", len(infos), err)
				}
			case "read-error":
				if !errors.Is(err, sentinel) {
					t.Fatalf("read error=%v want %v", err, sentinel)
				}
			case "canceled-before", "canceled-between":
				wantCalls := 0
				if mode == "canceled-between" {
					wantCalls = 1
				}
				if !errors.Is(err, context.Canceled) || calls != wantCalls {
					t.Fatalf("calls=%d want %d err=%v", calls, wantCalls, err)
				}
			case "empty-page":
				if !errors.Is(err, io.ErrNoProgress) {
					t.Fatalf("empty page: err=%v", err)
				}
			}
		})
	}
	// Hooks are context scoped; another rclone consumer keeps upstream behavior.
	_, err := smb.ReadDir(t.Context(), "", func(n int) ([]os.FileInfo, error) {
		if n != -1 {
			t.Fatalf("unscoped Readdir(%d) want -1", n)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSMBListingRawNames(t *testing.T) {
	for _, test := range []struct {
		dir, name string
		safe      bool
	}{
		{name: "../escape"}, {name: "NUL.fit"}, {name: "deep/file.fit"},
		{dir: "nested", name: "../escape"}, {dir: "nested", name: "NUL.fit"},
		{dir: "nested", name: "SHA256SUMS", safe: true},
		{dir: "nested", name: "file.fit", safe: true},
	} {
		t.Run(test.dir+"/"+test.name, func(t *testing.T) {
			budget := &listingBudget{}
			ctx := smb.WithReadDir(t.Context(), budget.readDir)
			_, err := smb.ReadDir(ctx, test.dir, func(int) ([]os.FileInfo, error) {
				return []os.FileInfo{listingInfo{test.name}}, io.EOF
			})
			if test.safe {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireReason(t, err, "unsafe_path")
			}
		})
	}
}
