package keylog

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// Literal NSS fixture: the writer must preserve the library's bytes verbatim.
const nss = "CLIENT_RANDOM 000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f 000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f202122232425262728292a2b2c2d2e2f\n"

func TestExclusivePrivateAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys")
	w, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	st, _ := os.Stat(path)
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := w.Write([]byte(nss)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	w.Close()
	b, err := os.ReadFile(path) // Drain preserves complete appended lines.
	if err != nil || !bytes.Equal(b, bytes.Repeat([]byte(nss), 8)) {
		t.Fatal("line loss or interleaving", err)
	}
	for _, mode := range []os.FileMode{0600, 0644} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if other, err := Open(path, nil); err == nil {
			other.Close()
			t.Fatal("existing file accepted")
		}
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if other, err := Open(link, nil); err == nil {
		other.Close()
		t.Fatal("symlink accepted")
	}
}
