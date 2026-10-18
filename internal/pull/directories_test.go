package pull

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/object"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func TestDirectoryValidation(t *testing.T) {
	for _, dirs := range [][]string{
		{""}, {"/MyWorks"}, {"../MyWorks"}, {"MyWorks/../Dark"}, {"MyWorks//Dark"}, {"MyWorks/"}, {`Autorun\Dark`},
		{"MyWorks", "MyWorks"}, {"MyWorks", "MyWorks/Dark"}, {"MyWorks/Dark", "MyWorks"},
		{"MyWorks", "myworks"}, {"MyWorks/A", "myworks/B"}, {"é/A", "é/B"},
		{".seestar-wormhole"}, {"SHA256SUMS"}, {"a/.seestar-part-x"}, {"CON"}, {"a/NUL.fit"}, {"a."}, {"a "}, {"a:b"}, {"a\n"},
	} {
		t.Run(dirs[0], func(t *testing.T) {
			c := DefaultConfig()
			c.Destination = t.TempDir()
			c.Dirs = dirs
			if err := c.Validate("linux"); err == nil {
				t.Fatalf("accepted dirs %q", dirs)
			}
		})
	}
	c := DefaultConfig()
	c.Destination = t.TempDir()
	c.Dirs = []string{"MyWorks", "Autorun/Dark", "Autorun/Flat"}
	if err := c.Validate("linux"); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryLayoutAndCheckpoints(t *testing.T) {
	c, d, src, root, now := fixture(t)
	// fixture's binding has not been persisted yet; start an explicit archive.
	d.State.Source = ""
	c.Dirs = []string{"MyWorks", "Autorun/Dark"}
	if err := d.BindLayout(c.Dirs); err != nil {
		t.Fatal(err)
	}
	if err := d.BindSource("test-source"); err != nil {
		t.Fatal(err)
	}
	putFile(t, root, "MyWorks/target/a.fit", "light", now.Add(-time.Hour))
	putFile(t, root, "Autorun/Dark/sequence/b.fit", "dark", now.Add(-time.Hour))
	b := runBatch(t, d, c, src, now)
	if b.Result.Copied != 2 || len(d.State.Files) != 2 || len(d.State.Selections) != 2 {
		t.Fatalf("batch=%+v state=%+v", b.Result, d.State)
	}
	for _, dir := range c.Dirs {
		if d.State.Selections[directorySelectionKey(dir, nil)].Checkpoint.IsZero() {
			t.Fatalf("no checkpoint for %s", dir)
		}
	}
	if err := d.BindLayout(nil); err == nil {
		t.Fatal("accepted legacy layout")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDestination(c.Destination)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.BindLayout(c.Dirs); err != nil {
		t.Fatal(err)
	}
	// Adding an old file in a new directory must not inherit another checkpoint.
	putFile(t, root, "Autorun/Flat/sequence/c.fit", "flat", now.Add(-24*time.Hour))
	c.Dirs = []string{"Autorun/Flat"}
	b = runBatch(t, reopened, c, src, now.Add(time.Hour))
	if b.Result.Copied != 1 || !b.Result.FirstScan {
		t.Fatalf("added dir: %+v", b.Result)
	}
	c.Dirs = []string{"MyWorks", "Autorun/Dark", "Autorun/Flat"}
	b = runBatch(t, reopened, c, src, now.Add(2*time.Hour))
	if b.Result.Copied != 0 || b.Result.FirstScan {
		t.Fatalf("repeat: %+v", b.Result)
	}
}

func TestDirectoryCheckpointDoesNotInheritAnotherRoot(t *testing.T) {
	c, d, src, root, now := fixture(t)
	d.State.Source = ""
	c.Dirs = []string{"MyWorks"}
	if err := d.BindLayout(c.Dirs); err != nil {
		t.Fatal(err)
	}
	if err := d.BindSource("test-source"); err != nil {
		t.Fatal(err)
	}
	putFile(t, root, "MyWorks/a.fit", "light", now.Add(-time.Hour))
	putFile(t, root, "Autorun/Dark/b.fit", "dark", now.Add(-time.Hour))
	runBatch(t, d, c, src, now)
	c.Dirs = []string{"Autorun/Dark"}
	c.Since = "all"
	runBatch(t, d, c, src, now)
	c.Dirs = []string{"MyWorks", "Autorun/Dark"}
	c.Since = "auto"
	c.Recheck = true
	b := runBatch(t, d, c, src, now.Add(time.Minute))
	// MyWorks has a recent checkpoint and skips its older matching receipt.
	// Dark has a receipt but no auto checkpoint and must compare its old bytes.
	if b.Result.Verified != 1 || !b.Result.FirstScan || !b.Result.Bounds.Lower.IsZero() {
		t.Fatalf("new directory inherited window: %+v", b.Result)
	}
}

func TestDirectorySafetyAcrossRoots(t *testing.T) {
	for _, mode := range []string{"unsafe", "case", "empty-case", "local-case", "symlink", "future"} {
		t.Run(mode, func(t *testing.T) {
			c, d, src, root, now := fixture(t)
			d.State.Source = ""
			c.Dirs = []string{"MyWorks", "Autorun/Dark"}
			if err := d.BindLayout(c.Dirs); err != nil {
				t.Fatal(err)
			}
			if err := d.BindSource("test-source"); err != nil {
				t.Fatal(err)
			}
			putFile(t, root, "MyWorks/ok.fit", "good", now.Add(-time.Hour))
			putFile(t, root, "Autorun/Dark/ok.fit", "dark", now.Add(-time.Hour))
			want := "unsafe_path"
			switch mode {
			case "unsafe":
				putFile(t, root, "Autorun/Dark/NUL.fit", "bad", now.Add(-time.Hour))
			case "case":
				putFile(t, root, "Autorun/Dark/OK.fit", "bad", now.Add(-time.Hour))
				want = "case_collision"
			case "empty-case":
				for _, name := range []string{"Autorun/Dark/empty", "Autorun/Dark/EMPTY"} {
					if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
						t.Fatal(err)
					}
				}
				want = "case_collision"
			case "local-case":
				if err := os.Mkdir(filepath.Join(c.Destination, "autorun"), 0700); err != nil {
					t.Fatal(err)
				}
				want = "case_collision"
			case "symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(c.Destination, "Autorun")); err != nil {
					t.Skip(err)
				}
			case "future":
				putFile(t, root, "Autorun/Dark/future.fit", "bad", time.Now().Add(time.Hour))
				want = "future_mtime"
			}
			_, err := d.Run(t.Context(), c, now, src)
			_, reason := diagnostic.Fields(err, "pull")
			if reason != want {
				t.Fatalf("reason=%s want %s err=%v", reason, want, err)
			}
			if len(d.State.Files) != 0 || len(d.State.Selections) != 0 {
				t.Fatalf("unsafe batch modified receipts/checkpoints: %+v", d.State)
			}
		})
	}
}

type directoryBudgetFs struct{ fs.Fs }

func (f directoryBudgetFs) List(_ context.Context, dir string) (fs.DirEntries, error) {
	entries := make(fs.DirEntries, maxEntries/2)
	entry := object.NewMemoryObject(dir+"/file.fit", time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), []byte("x"))
	for i := range entries {
		entries[i] = entry
	}
	return entries, nil
}

func TestDirectoryInventoryBudgetIsAggregate(t *testing.T) {
	c, d, src, _, now := fixture(t)
	d.State.Source = ""
	c.Dirs = []string{"A", "B"}
	if err := d.BindLayout(c.Dirs); err != nil {
		t.Fatal(err)
	}
	if err := d.BindSource("test-source"); err != nil {
		t.Fatal(err)
	}
	_, err := d.Run(t.Context(), c, now, directoryBudgetFs{src})
	_, reason := diagnostic.Fields(err, "pull")
	if reason != "inventory_limit" {
		t.Fatalf("reason=%s err=%v", reason, err)
	}
}

func TestDirectoryProgressAndSettling(t *testing.T) {
	c, d, src, root, now := fixture(t)
	d.State.Source = ""
	c.Dirs = []string{"MyWorks", "Autorun/Dark"}
	if err := d.BindLayout(c.Dirs); err != nil {
		t.Fatal(err)
	}
	if err := d.BindSource("test-source"); err != nil {
		t.Fatal(err)
	}
	putFile(t, root, "MyWorks/a.fit", "light", now.Add(-time.Hour))
	putFile(t, root, "Autorun/Dark/b.fit", "dark", now.Add(-time.Hour))
	putFile(t, root, "Autorun/Dark/recent.fit", "recent", now.Add(-time.Minute))
	ctx, p := NewProgress(t.Context())
	c.Progress = p
	c.Verify = true
	b, err := d.Run(ctx, c, now, src)
	if err != nil {
		t.Fatal(err)
	}
	if b.Result.Copied != 2 || b.Result.Verified != 2 || b.Result.Deferred != 1 || b.Result.Bytes != 9 {
		t.Fatalf("result=%+v", b.Result)
	}
	if got := p.Snapshot(); got.FilesDone != 2 || got.FilesTotal != 2 || got.BytesDone != 27 || got.BytesTotal != 27 {
		t.Fatalf("progress=%+v", got)
	}
	for path, receipt := range d.State.Files {
		if receipt.Check != checkReread {
			t.Fatalf("%s receipt=%+v", path, receipt)
		}
	}
	if len(d.State.Selections) != 0 {
		t.Fatal("Run committed checkpoints before release")
	}
	if err := b.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDirectorySMBUsesShareRoot(t *testing.T) {
	c := DefaultConfig()
	c.Dirs = []string{"MyWorks", "Autorun/Dark"}
	ctx := Configure(t.Context(), c)
	_, src, err := OpenSMB(ctx, func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("opening a share root should not dial before inventory")
		return nil, errors.New("unexpected dial")
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.Root() != "EMMC Images" {
		t.Fatalf("SMB root=%q want EMMC Images", src.Root())
	}
	if err := src.Features().Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}
