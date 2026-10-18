package pull

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// These literal files were produced by the default layout before --dir existed, with this same workload.
// Fix the random generation to compare the entire state, including receipts.
func TestDefaultLayoutGoldenFromMain(t *testing.T) {
	root, dest := t.TempDir(), t.TempDir()
	started := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"root.fit", "M 31/frame.fit"} {
		putFile(t, root, name, "independent bytes\n", started.Add(-time.Hour))
	}
	ctx := context.Background()
	src, err := local.NewFs(ctx, "golden-source", root, configmap.Simple{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.State.Generation = "0123456789abcdef0123456789abcdef"
	if err = d.BindSource("direct:golden"); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.Destination = dest
	c.Transfers = 1
	for pass := range 2 {
		b, err := d.Run(ctx, c, started, src)
		if err != nil {
			t.Fatal(err)
		}
		if err = b.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{".seestar-wormhole/state.json", "SHA256SUMS"} {
			got, err := os.ReadFile(filepath.Join(dest, name))
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata/default-main", filepath.Base(name)))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("pass %d %s differs from main\ngot:\n%s\nwant:\n%s", pass, name, got, want)
			}
		}
	}
}

func TestDefaultPreflightKeepsFailureOrder(t *testing.T) {
	c, d, src, root, now := fixture(t)
	putFile(t, root, "A/a.fit", "a", now.Add(-time.Hour))
	putFile(t, root, "a/b.fit", "b", now.Add(-time.Hour))
	if err := os.Symlink(t.TempDir(), filepath.Join(c.Destination, "A")); err != nil {
		t.Skip(err)
	}
	_, err := d.Run(t.Context(), c, now, src)
	_, reason := diagnostic.Fields(err, "pull")
	if reason != "unsafe_path" {
		t.Fatalf("first preflight failure=%s want unsafe_path", reason)
	}
}
