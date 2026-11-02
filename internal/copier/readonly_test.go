package copier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/readonly"
)

// OpenDest stats like Copier.Stat, and a missing destination is empty and
// not created.
func TestOpenDest(t *testing.T) {
	mtime := time.Date(2026, 10, 29, 3, 31, 19, 0, time.UTC)
	dest := filepath.Join(t.TempDir(), "dest")
	writeFile(t, filepath.Join(dest, "MyWorks", "a.fit"), "12345")
	if err := os.Chtimes(filepath.Join(dest, "MyWorks", "a.fit"), time.Time{}, mtime); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDest(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if got := d.Stat("MyWorks/a.fit"); !got.Exists || !got.Regular || got.Size != 5 || !got.ModTime.Equal(mtime) || got.Err != nil {
		t.Errorf("Stat(MyWorks/a.fit) = %+v, want a regular file of 5 bytes with mtime %v", got, mtime)
	}
	if got := d.Stat("MyWorks/missing.fit"); got.Exists || got.Err != nil {
		t.Errorf("Stat(MyWorks/missing.fit) = %+v, want not existing", got)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	d, err = OpenDest(missing)
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Stat("MyWorks/a.fit"); got.Exists || got.Err != nil {
		t.Errorf("Stat in a missing destination = %+v, want not existing", got)
	}
	if err := d.Close(); err != nil {
		t.Error(err)
	}
	if _, err := os.Lstat(missing); err == nil {
		t.Error("OpenDest created the destination")
	}
}

// After readonly.Enable, every function of the copier that writes panics,
// and nothing reaches the disk.
func TestWritesPanicWhenReadOnly(t *testing.T) {
	fx := newFixture(t, func(dest string) {
		writeFile(t, filepath.Join(dest, "MyWorks", plan.TempName("MyWorks/a.fit")), "leftover")
	})
	c := &Copier{root: openRoot(t, fx.dest), state: fx.st, now: time.Now}
	before := fx.records(t)
	readonly.Enable()
	t.Cleanup(readonly.Reset)
	d := decision(plan.Copy, "MyWorks/b.fit", 3)
	for name, f := range map[string]func(){
		"copier.New":         func() { New(fx.dest, fx.st, nil) },
		"copier.removeTemps": func() { removeTemps(c.root, nil) },
		"copier.Run":         func() { c.Run(t.Context(), device(nil), 1, []plan.Decision{d}, func(Result) {}) },
		"copier.download":    func() { c.download(t.Context(), device(nil), d, "copied") },
		"copier.adopt":       func() { c.adopt(t.Context(), d) },
		"copier.record":      func() { c.record(d, strings.Repeat("a", 64), "copied") },
		"copier.syncFolder":  func() { c.syncFolder("MyWorks") },
	} {
		func() {
			defer func() {
				if r, _ := recover().(string); !strings.Contains(r, name+" would change the local filesystem") {
					t.Errorf("%s: panic %q, want the read-only guard", name, r)
				}
			}()
			f()
		}()
	}
	if _, err := os.Lstat(filepath.Join(fx.dest, "MyWorks", plan.TempName("MyWorks/a.fit"))); err != nil {
		t.Errorf("the leftover temporary file is gone: %v", err)
	}
	if got := fx.records(t); len(got) != len(before) {
		t.Errorf("state records changed to %q", got)
	}
}

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root
}
