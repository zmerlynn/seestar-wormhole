package pull

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func requireReason(t *testing.T, err error, want string) {
	t.Helper()
	_, got := diagnostic.Fields(Failure(err), "pull")
	if err == nil || got != want {
		t.Fatalf("reason=%s want %s (error type %T)", got, want, err)
	}
}

type listingFs struct {
	fs.Fs
	list func(context.Context, string) (fs.DirEntries, error)
}

func (f listingFs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	return f.list(ctx, dir)
}

type invalidMetadataObject struct{ fs.Object }

func (invalidMetadataObject) ModTime(context.Context) time.Time { return time.Time{} }

func TestPullFailureInventoryAndMetadata(t *testing.T) {
	for _, reason := range []string{"inventory_limit", "invalid_metadata", "unsafe_path"} {
		t.Run(reason, func(t *testing.T) {
			c, d, src, root, now := fixture(t)
			putFile(t, root, "private.fit", "A", now.Add(-time.Hour))
			wrapped := listingFs{Fs: src, list: func(ctx context.Context, dir string) (fs.DirEntries, error) {
				entries, err := src.List(ctx, dir)
				if err != nil {
					return nil, err
				}
				if len(entries) != 1 {
					t.Fatalf("fixture entries=%d want 1", len(entries))
				}
				switch reason {
				case "inventory_limit":
					many := make(fs.DirEntries, maxEntries+1)
					for i := range many {
						many[i] = entries[0]
					}
					return many, nil
				case "invalid_metadata":
					return fs.DirEntries{invalidMetadataObject{entries[0].(fs.Object)}}, nil
				default:
					return fs.DirEntries{fs.NewDir("private.", now)}, nil
				}
			}}
			_, err := d.Run(context.Background(), c, now, wrapped)
			requireReason(t, err, reason)
			if len(d.State.Files) != 0 || len(d.State.Selections) != 0 {
				t.Fatal("refusal changed state")
			}
		})
	}
}

func TestPullFailureMetadataLimit(t *testing.T) {
	dest := t.TempDir()
	d, err := OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()
	state := filepath.Join(dest, metadata, "state.json")
	f, err := os.OpenFile(state, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxStateBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, err = OpenDestination(dest)
	requireReason(t, err, "metadata_limit")
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	d, err = OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.State.Source = "test-source"
	d.State.Selections["too-large"] = Selection{Only: []string{strings.Repeat("x", maxStateBytes)}}
	requireReason(t, d.save(), "metadata_limit")
}

func TestPullFailureLock(t *testing.T) {
	dest := t.TempDir()
	d, err := OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	_, err = OpenDestination(dest)
	requireReason(t, err, "lock_held")
	if !errors.Is(err, ErrLocked) {
		t.Fatal("lost lock exit policy")
	}
}

func TestPullFailureDiskFull(t *testing.T) {
	if os.Getenv("SEESTAR_TEST_SAMBA_ADDR") == "" {
		t.Skip("container Samba and private /dev/shm fixture required")
	}
	c, _, src, root, now := fixture(t)
	dest, err := os.MkdirTemp("/dev/shm", "pull-failure-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dest)
	d, err := OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.BindSource("test-source")
	if err := d.save(); err != nil {
		t.Fatal(err)
	}
	c.Destination = dest
	putFile(t, root, "private.fit", strings.Repeat("A", 128<<10), now.Add(-time.Hour))
	filler, err := os.CreateTemp("/dev/shm", "pull-filler-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filler.Name())
	defer filler.Close()
	chunk := make([]byte, 1<<20)
	for i := 0; ; i++ {
		if i > 1024 {
			t.Fatal("fixture exceeds 1 GiB; expected bounded tmpfs")
		}
		if _, err = filler.Write(chunk); err != nil {
			break
		}
	}
	requireReason(t, err, "disk_full")
	_, err = d.Run(context.Background(), c, now, src)
	requireReason(t, err, "disk_full")
	if len(d.State.Files) != 0 || len(d.State.Selections) != 0 {
		t.Fatal("disk-full transfer changed receipts/checkpoint")
	}
	if err := filler.Truncate(0); err != nil {
		t.Fatal(err)
	}
	b := runBatch(t, d, c, src, now)
	if b.Result.Copied != 1 {
		t.Fatal("retry after freeing space did not complete")
	}
}
