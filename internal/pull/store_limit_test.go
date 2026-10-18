package pull

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStateByteLimit(t *testing.T) {
	const entries = 130000
	root := t.TempDir()
	d, err := OpenDestination(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if d != nil {
			d.Close()
		}
	}()
	d.State.Source = "direct:" + strings.Repeat("0", 64)
	d.State.Generation = strings.Repeat("0", 32)
	receipt := Receipt{Source: d.State.Source, Generation: d.State.Generation,
		Mtime:  time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		SHA256: strings.Repeat("0", 64), Check: checkSize}
	for i := range entries {
		d.State.Files[fmt.Sprintf("f%06d", i)] = receipt
	}
	if err := d.save(); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(root, metadata, "state.json")
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	// Measure real output so the boundary fixture does not duplicate encoder options.
	extra := maxStateBytes - int(info.Size())
	if extra < 0 || entries > maxEntries {
		t.Fatal("fixture baseline exceeds limits")
	}
	clear(d.State.Files)
	pathBytes := 0
	for i := range entries {
		n := extra / entries
		if i < extra%entries {
			n++
		}
		name := strings.Repeat("a", n) + fmt.Sprintf("f%06d", i)
		if len(name) > 255 || validateRemote(name) != nil {
			t.Fatal("fixture name is not portable")
		}
		d.State.Files[name] = receipt
		pathBytes += len(name)
	}
	if pathBytes+1 > maxListingBytes {
		t.Fatal("fixture exceeds source path budget")
	}
	if err := d.save(); err != nil {
		t.Fatalf("exact-limit save: %v", err)
	}
	before, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != maxStateBytes {
		t.Fatalf("persisted bytes = %d, want %d", len(before), maxStateBytes)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = OpenDestination(root)
	if err != nil {
		t.Fatalf("exact-limit reopen: %v", err)
	}
	t.Logf("exact-limit state saves and reopens: %d bytes", len(before))
	// One extra ASCII path byte makes the persisted representation one byte over.
	for name, receipt := range d.State.Files {
		delete(d.State.Files, name)
		d.State.Files["a"+name] = receipt
		break
	}
	requireReason(t, d.save(), "metadata_limit")
	after, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("overflow replaced previously committed state")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = OpenDestination(root)
	if err != nil {
		t.Fatalf("reopen after overflow refusal: %v", err)
	}
	t.Logf("%d-byte state refused; prior bytes unchanged and readable", maxStateBytes+1)
}
