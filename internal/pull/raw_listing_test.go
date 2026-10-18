package pull

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/object"
)

func TestRawListingRefusal(t *testing.T) {
	for _, test := range []struct{ name, dir, remote string }{
		{"parent", "", "../escape"},
		{"nonportable", "", "NUL.fit"},
		{"root-nonmember", "", "elsewhere/child"},
		{"sibling", "listed", "sibling/file"},
		{"descendant", "listed", "listed/deep/file"},
		{"self", "listed", "listed"},
	} {
		for _, directory := range []bool{false, true} {
			kind := "object"
			if directory {
				kind = "directory"
			}
			t.Run(test.name+"/"+kind, func(t *testing.T) {
				c, d, src, _, now := fixture(t)
				// Raw safety applies even to entries excluded by --only.
				c.Only = []string{"selected"}
				key := directorySelectionKey("", c.Only)
				checkpoint := now.Add(-24 * time.Hour)
				d.State.Selections[key] = Selection{Only: c.Only, Checkpoint: checkpoint}
				if err := d.save(); err != nil {
					t.Fatal(err)
				}
				statePath := filepath.Join(c.Destination, metadata, "state.json")
				before, err := os.ReadFile(statePath)
				if err != nil {
					t.Fatal(err)
				}
				wrapped := listingFs{Fs: src, list: func(_ context.Context, dir string) (fs.DirEntries, error) {
					if dir != test.dir {
						return fs.DirEntries{fs.NewDir("listed", now)}, nil
					}
					if directory {
						return fs.DirEntries{fs.NewDir(test.remote, now)}, nil
					}
					return fs.DirEntries{object.NewMemoryObject(test.remote, now.Add(-time.Hour), []byte("x"))}, nil
				}}
				b, err := d.Run(t.Context(), c, now, wrapped)
				if err == nil {
					t.Fatalf("unsafe raw listing accepted: status=%s", b.Result.Status)
				}
				requireReason(t, err, "unsafe_path")
				if len(d.State.Files) != 0 || !d.State.Selections[key].Checkpoint.Equal(checkpoint) {
					t.Fatal("refusal changed receipts/checkpoint")
				}
				after, err := os.ReadFile(statePath)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("refusal changed saved state: err=%v", err)
				}
			})
		}
	}
}
