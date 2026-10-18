package pull

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/backend/smb"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/lib/encoder"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func fixture(t *testing.T) (Config, *Destination, fs.Fs, string, time.Time) {
	t.Helper()
	root := t.TempDir()
	dest := t.TempDir()
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	c := DefaultConfig()
	c.Destination = dest
	c.Transfers = 1
	var source fs.Fs
	var err error
	if addr := os.Getenv("SEESTAR_TEST_SAMBA_ADDR"); addr != "" {
		root, err = os.MkdirTemp("/tmp/smb-e2e/share/MyWorks", "pull-test-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(root) })
		if err = os.Chmod(root, 0755); err != nil {
			t.Fatal(err)
		}
		if err == nil {
			// Scope each fixture to its own immediate folder through rclone's SMB root.
			// NewFs is used explicitly; the transport remains the production context hook.
			source, err = openFixtureSMB(addr, filepath.Base(root))
		}
	} else {
		source, err = local.NewFs(context.Background(), "fixture", root, configmap.Simple{})
	}
	if err != nil {
		t.Fatal(err)
	}
	if source.Features().Shutdown != nil {
		t.Cleanup(func() { source.Features().Shutdown(context.Background()) })
	}
	d, err := OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err = d.BindSource("test-source"); err != nil {
		t.Fatal(err)
	}
	return c, d, source, root, now
}
func putFile(t *testing.T, root, name, bytes string, mtime time.Time) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(bytes), 0666); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SEESTAR_TEST_SAMBA_ADDR") != "" && strings.HasPrefix(root, "/tmp/smb-e2e/share/MyWorks/") {
		// Samba runs as nobody; make synthetic source readable independently of
		// the destination data-umask test. No delivery permissions are modified.
		if err := os.Chmod(filepath.Dir(filename), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filename, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(filename, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}
func runBatch(t *testing.T, d *Destination, c Config, src fs.Fs, now time.Time) *Batch {
	t.Helper()
	b, err := d.Run(context.Background(), c, now, src)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFirstEmptySettleOverlapAndLateArrival(t *testing.T) {
	c, d, src, root, now := fixture(t)
	putFile(t, root, "Dark/a.fit", "independent literal fixture\n", now.Add(-time.Hour))
	putFile(t, root, "Dark_sub/b.fit", "still writing", now.Add(-time.Minute))
	b := runBatch(t, d, c, src, now)
	if b.Result.Copied != 1 || b.Result.Deferred != 1 || b.Result.Status != "copied" || !b.Result.FirstScan {
		t.Fatalf("first=%+v", b.Result)
	}
	// Settle exceeds overlap: the deferred file must still be picked up.
	c.Overlap = 0
	c.Settle = 10 * time.Minute
	b = runBatch(t, d, c, src, now.Add(15*time.Minute))
	if b.Result.Copied != 1 {
		t.Fatalf("settle pickup=%+v", b.Result)
	}
	c.Settle = 2 * time.Minute
	c.Overlap = 2 * time.Hour
	b = runBatch(t, d, c, src, now.Add(20*time.Minute))
	if b.Result.Copied != 0 || b.Result.Verified != 0 || b.Result.Status != "nothing-new" {
		t.Fatalf("overlap=%+v", b.Result)
	}
	// Backdated arrival, changed metadata and local deletion bypass auto lower.
	c.Overlap = 0
	putFile(t, root, "Dark/late.fit", "backdated", now.Add(-24*time.Hour))
	putFile(t, root, "Dark/a.fit", "changed size and metadata", now.Add(-2*time.Hour))
	if err := os.Remove(filepath.Join(c.Destination, "Dark_sub/b.fit")); err != nil {
		t.Fatal(err)
	}
	b = runBatch(t, d, c, src, now.Add(time.Hour))
	if b.Result.Copied != 3 {
		t.Fatalf("late/changed/missing=%+v", b.Result)
	}
	sums, err := os.ReadFile(filepath.Join(c.Destination, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.Split(strings.TrimSpace(string(sums)), "\n")) != 3 || strings.Contains(string(sums), ".seestar") {
		t.Fatalf("manifest=%s", sums)
	}
}

func TestSelectionNamespaceAndExplicitSince(t *testing.T) {
	c, d, src, root, now := fixture(t)
	putFile(t, root, "Dark/a.fit", "A", now.Add(-time.Hour))
	putFile(t, root, "Light/a.fit", "B", now.Add(-2*time.Hour))
	c.Only = []string{"Dark.*"}
	b := runBatch(t, d, c, src, now)
	if b.Result.Copied != 1 {
		t.Fatal(b.Result)
	}
	c.Only = []string{"Light.*"}
	b = runBatch(t, d, c, src, now.Add(time.Hour))
	if !b.Result.FirstScan || b.Result.Copied != 1 {
		t.Fatal(b.Result)
	}
	checkpoint := d.State.Selections[selectionKey(c.Only)].Checkpoint
	c.Since = "all"
	runBatch(t, d, c, src, now.Add(2*time.Hour))
	if !d.State.Selections[selectionKey(c.Only)].Checkpoint.Equal(checkpoint) {
		t.Fatal("explicit all advanced auto checkpoint")
	}
	c.Only = []string{"Nothing.*"}
	if b, err := d.Run(context.Background(), c, now, src); err != nil || b.Result.Status != "nothing-new" {
		t.Fatalf("unmatched expression: batch=%+v err=%v", b, err)
	}
}

type injectedFs struct {
	fs.Fs
	inject func(context.Context, fs.Object) fs.Object
}

func (f injectedFs) NewObject(ctx context.Context, path string) (fs.Object, error) {
	o, err := f.Fs.NewObject(ctx, path)
	if err != nil {
		return nil, err
	}
	return f.inject(ctx, o), nil
}

type injectedObject struct {
	fs.Object
	open func(context.Context, ...fs.OpenOption) (io.ReadCloser, error)
}

func (o injectedObject) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	return o.open(ctx, options...)
}

func TestInterruptedReceiptsAndNoCheckpoint(t *testing.T) {
	c, d, src, root, now := fixture(t)
	putFile(t, root, "Dark/a.fit", "first", now.Add(-time.Hour))
	putFile(t, root, "Dark/b.fit", "second", now.Add(-time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broken := injectedFs{src, func(_ context.Context, o fs.Object) fs.Object {
		if o.Remote() != "Dark/b.fit" {
			return o
		}
		return injectedObject{o, func(context.Context, ...fs.OpenOption) (io.ReadCloser, error) { cancel(); return nil, context.Canceled }}
	}}
	if _, err := d.Run(ctx, c, now, broken); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupt=%v", err)
	}
	if len(d.State.Selections) != 0 || len(d.State.Files) != 1 {
		t.Fatalf("checkpoint/receipts=%+v", d.State)
	}
	b := runBatch(t, d, c, src, now)
	if b.Result.Copied != 1 || b.Result.Verified != 1 {
		t.Fatalf("rerun=%+v", b.Result)
	}
	c.Sums = false
	b, err := d.Run(context.Background(), c, now.Add(time.Hour), src)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	before := d.State.Selections[selectionKey(c.Only)].Checkpoint
	if err := b.Commit(ctx); !errors.Is(err, context.Canceled) || !d.State.Selections[selectionKey(c.Only)].Checkpoint.Equal(before) {
		t.Fatal("late cancellation committed checkpoint")
	}
}

func TestCorruptionVerifiedBeforeReplacementEvenWithoutSums(t *testing.T) {
	c, d, src, root, now := fixture(t)
	c.Verify = true
	c.Sums = false
	putFile(t, root, "Dark/a.fit", "source correct", now.Add(-time.Hour))
	putFile(t, c.Destination, "Dark/a.fit", "prior good archive", now.Add(-2*time.Hour))
	reads := 0
	broken := injectedFs{src, func(_ context.Context, o fs.Object) fs.Object {
		return injectedObject{o, func(ctx context.Context, opts ...fs.OpenOption) (io.ReadCloser, error) {
			reads++
			if reads == 2 {
				return io.NopCloser(strings.NewReader("source corrupt")), nil
			}
			return o.Open(ctx, opts...)
		}}
	}}
	if _, err := d.Run(context.Background(), c, now, broken); err == nil {
		t.Fatal("corrupt download accepted")
	} else {
		_, reason := diagnostic.Fields(err, "pull")
		if reason != "verify_mismatch" {
			t.Fatalf("corruption reason=%s want verify_mismatch", reason)
		}
	}
	data, err := os.ReadFile(filepath.Join(c.Destination, "Dark/a.fit"))
	if err != nil || string(data) != "prior good archive" {
		t.Fatalf("old archive replaced: %q %v", data, err)
	}
	if len(d.State.Selections) != 0 || len(d.State.Files) != 0 {
		t.Fatal("corrupt bytes receipted")
	}
	b := runBatch(t, d, c, src, now)
	if b.Result.Copied != 1 {
		t.Fatal(b.Result)
	}
	if _, err = os.Stat(filepath.Join(c.Destination, "SHA256SUMS")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sums=false: %v", err)
	}
	// Identical-metadata local corruption requires an explicit recheck.
	putFile(t, c.Destination, "Dark/a.fit", "source corrupt", now.Add(-time.Hour))
	c.Since = "all"
	c.Recheck = true
	b = runBatch(t, d, c, src, now.Add(time.Minute))
	if b.Result.Copied != 1 {
		t.Fatal("corrupt existing bytes reused", b.Result)
	}
}

func TestFutureMtimeAndCollisionFailBeforeCopy(t *testing.T) {
	for _, test := range []string{"future", "case", "symlink"} {
		t.Run(test, func(t *testing.T) {
			c, d, src, root, now := fixture(t)
			putFile(t, root, "Dark/a.fit", "A", now.Add(-time.Hour))
			switch test {
			case "future":
				putFile(t, root, "Dark/b.fit", "B", time.Now().Add(time.Hour))
			case "case":
				putFile(t, root, "dark/b.fit", "B", now.Add(-time.Hour))
			case "symlink":
				if err := os.Symlink(t.TempDir(), filepath.Join(c.Destination, "Dark")); err != nil {
					t.Skip(err)
				}
			}
			if _, err := d.Run(context.Background(), c, now, src); err == nil {
				t.Fatal("unsafe batch accepted")
			} else {
				want := map[string]string{"future": "future_mtime", "case": "case_collision", "symlink": "unsafe_path"}[test]
				_, reason := diagnostic.Fields(err, "pull")
				if reason != want {
					t.Fatalf("reason=%s want %s", reason, want)
				}
			}
			if len(d.State.Files) != 0 || len(d.State.Selections) != 0 {
				t.Fatal("unsafe batch changed state")
			}
		})
	}
}

func openFixtureSMB(addr, folder string) (fs.Fs, error) {
	ctx := smb.WithDialContext(context.Background(), func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	})
	source, err := smb.NewFs(ctx, "fixture", "EMMC Images/MyWorks/"+folder, configmap.Simple{"host": "device.invalid", "user": "guest", "idle_timeout": "0", "encoding": encoder.EncodeZero.String()})
	// NewFs stores ctx only for setup; subsequent operations must carry the hook.
	return contextFs{Fs: source}, err
}

type contextFs struct{ fs.Fs }

func (f contextFs) List(ctx context.Context, p string) (fs.DirEntries, error) {
	return f.Fs.List(smb.WithDialContext(ctx, f.dial), p)
}
func (f contextFs) NewObject(ctx context.Context, p string) (fs.Object, error) {
	return f.Fs.NewObject(smb.WithDialContext(ctx, f.dial), p)
}
func (f contextFs) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", os.Getenv("SEESTAR_TEST_SAMBA_ADDR"))
}

func TestSelectionRegexpFolders(t *testing.T) {
	c, d, src, root, now := fixture(t)
	c.Recheck = true
	for _, folder := range []string{"M 31", "M 31_sub", "M 31 (wide)"} {
		putFile(t, root, folder+"/a.fit", "bytes", now.Add(-time.Hour))
	}
	for _, tc := range []struct {
		only  []string
		count int
	}{
		{[]string{"M 31"}, 3},
		{[]string{"^M 31$"}, 1},
		{[]string{`M 31 \(wide\)`}, 1},
		{[]string{"unmatched", "^M 31$"}, 1},
		{[]string{"unmatched"}, 0},
	} {
		c.Only = tc.only
		if err := c.Validate("linux"); err != nil {
			t.Fatal(err)
		}
		b := runBatch(t, d, c, src, now)
		if b.Result.Verified != tc.count || !b.Result.FirstScan {
			t.Fatalf("%q: got %+v want %d verified, first scan", tc.only, b.Result, tc.count)
		}
	}
}
