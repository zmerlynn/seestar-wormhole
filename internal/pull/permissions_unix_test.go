//go:build !windows

package pull

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestDataUmaskAndPrivateState(t *testing.T) {
	previous := syscall.Umask(0027)
	defer syscall.Umask(previous)
	c, d, src, root, now := fixture(t)
	putFile(t, root, "Dark/a.fit", "data", now.Add(-3600000000000))
	b, err := d.Run(context.Background(), c, now, src)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{"Dark": 0750, "Dark/a.fit": 0640, "SHA256SUMS": 0640, metadata: 0700, metadata + "/state.json": 0600, metadata + "/lock": 0600} {
		info, err := os.Stat(filepath.Join(c.Destination, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s: mode %o want %o", p, info.Mode().Perm(), want)
		}
	}
}
