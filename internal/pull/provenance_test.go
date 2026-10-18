package pull

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestReviewRebindCannotAcceptOtherSourceReceipt(t *testing.T) {
	c, d, src, root, now := fixture(t)
	mtime := now.Add(-time.Hour)
	putFile(t, root, "Dark/a.fit", "AAAA", mtime)
	runBatch(t, d, c, src, now)
	if err := d.RebindSource("via:other-source:1445"); err != nil {
		t.Fatal(err)
	}
	// The replacement source has the same path, size and timestamp but different bytes.
	putFile(t, root, "Dark/a.fit", "BBBB", mtime)
	var reads atomic.Int64
	src = countedReadsFs{Fs: src, reads: &reads}
	// Restart after takeover: safety must survive a failed takeover invocation.
	d.Close()
	var err error
	d, err = OpenDestination(c.Destination)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	b := runBatch(t, d, c, src, now.Add(time.Hour))
	got, err := os.ReadFile(filepath.Join(c.Destination, "Dark/a.fit"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("source=BBBB local=%s reads=%d result=%+v checkpoint=%s", got, reads.Load(), b.Result, d.State.Selections[selectionKey(c.Only)].Checkpoint)
	if string(got) != "BBBB" || reads.Load() == 0 {
		t.Fatal("old-source receipt accepted under new binding without verification")
	}
}

func TestTakeoverRepairsOutsideCheckpointAndSelection(t *testing.T) {
	for _, binding := range []string{"direct:other", "via:other:4445", "test-source"} {
		t.Run(binding, func(t *testing.T) {
			c, d, src, root, now := fixture(t)
			oldTime := now.Add(-2 * time.Hour)
			putFile(t, root, "Dark/a.fit", "AAAA", oldTime)
			putFile(t, root, "Light/b.fit", "AAAA", oldTime)
			runBatch(t, d, c, src, now)
			if err := d.RebindSource(binding); err != nil {
				t.Fatal(err)
			}
			putFile(t, root, "Dark/a.fit", "BBBB", oldTime)
			putFile(t, root, "Light/b.fit", "BBBB", oldTime)
			c.Only = []string{"^Dark$"}
			runBatch(t, d, c, src, now.Add(time.Hour))
			// A restricted takeover must not relabel untouched Light receipts. Advance
			// the all-folder checkpoint past the files before repairing that selection.
			d.State.Selections[selectionKey(nil)] = Selection{Checkpoint: now}
			if err := d.save(); err != nil {
				t.Fatal(err)
			}
			d.Close()
			var err error
			d, err = OpenDestination(c.Destination)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			c.Only = nil
			c.Overlap = 0
			b := runBatch(t, d, c, src, now.Add(2*time.Hour))
			got, err := os.ReadFile(filepath.Join(c.Destination, "Light/b.fit"))
			if err != nil || string(got) != "BBBB" || b.Result.Verified != 1 {
				t.Fatalf("stale receipt outside window: %q %v %+v", got, err, b.Result)
			}
			var reads atomic.Int64
			b = runBatch(t, d, c, countedReadsFs{Fs: src, reads: &reads}, now.Add(3*time.Hour))
			if reads.Load() != 0 || b.Result.Verified != 0 {
				t.Fatalf("same generation reread: %d %+v", reads.Load(), b.Result)
			}
		})
	}
}

func TestLegacyReceiptsRepairOutsideCheckpoint(t *testing.T) {
	for _, version := range []int{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			c, d, src, root, now := fixture(t)
			putFile(t, root, "a.fit", "AAAA", now.Add(-time.Hour))
			runBatch(t, d, c, src, now)
			d.State.Version = version
			if version == 1 {
				d.State.Selections = map[string]Selection{legacySelectionKey(nil): {Checkpoint: now}}
			}
			if err := d.save(); err != nil {
				t.Fatal(err)
			}
			d.Close()
			var err error
			d, err = OpenDestination(c.Destination)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			putFile(t, root, "a.fit", "BBBB", now.Add(-time.Hour))
			c.Overlap = 0
			b := runBatch(t, d, c, src, now.Add(time.Hour))
			got, _ := os.ReadFile(filepath.Join(c.Destination, "a.fit"))
			if string(got) != "BBBB" || b.Result.Verified != 1 {
				t.Fatalf("legacy receipt trusted: %q %+v", got, b.Result)
			}
		})
	}
}

func TestRebindRetainsAttributionAcrossReturnAndFailure(t *testing.T) {
	c, d, src, root, now := fixture(t)
	putFile(t, root, "a.fit", "AAAA", now.Add(-time.Hour))
	runBatch(t, d, c, src, now)
	original := d.State.Files["a.fit"]
	for _, source := range []string{"via:one:445", "via:two:445", "direct:one", "test-source"} {
		if err := d.RebindSource(source); err != nil {
			t.Fatal(err)
		}
		d.Close()
		var err error
		d, err = OpenDestination(c.Destination)
		if err != nil {
			t.Fatal(err)
		}
		if d.State.Files["a.fit"] != original || d.State.Generation == original.Generation || len(d.State.Selections) != 0 {
			t.Fatalf("takeover relabeled receipt: %+v", d.State)
		}
	}
	defer d.Close()
	// Unknown source attribution under the current schema also needs repair.
	receipt := d.State.Files["a.fit"]
	receipt.Source = ""
	receipt.Generation = ""
	d.State.Files["a.fit"] = receipt
	var reads atomic.Int64
	b := runBatch(t, d, c, countedReadsFs{Fs: src, reads: &reads}, now.Add(time.Hour))
	if b.Result.Verified != 1 || reads.Load() == 0 {
		t.Fatalf("unknown receipt skipped: %+v", b.Result)
	}
	receipt = d.State.Files["a.fit"]
	if receipt.Source != d.State.Source || receipt.Generation != d.State.Generation {
		t.Fatalf("verified receipt not stamped: %+v", receipt)
	}
}
