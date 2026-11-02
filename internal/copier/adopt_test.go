package copier

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/plan"
)

// hookContext runs hook the first time Err is called. When adopt is called
// directly, the first call is hashExactly's, once the file has been opened
// and checked and its first bytes read.
type hookContext struct {
	context.Context
	once sync.Once
	hook func()
}

func (c *hookContext) Err() error {
	c.once.Do(c.hook)
	return c.Context.Err()
}

// writeLocal writes share path p in dest with the device mtime.
func writeLocal(t *testing.T, dest, p, data string) {
	t.Helper()
	name := filepath.Join(dest, filepath.FromSlash(p))
	writeFile(t, name, data)
	if err := os.Chtimes(name, time.Time{}, deviceMTime); err != nil {
		t.Fatal(err)
	}
}

// A local file that changes or is replaced while it is hashed is not
// adopted: the hash might not be of the file at the path.
func TestAdoptChangedWhileHashing(t *testing.T) {
	const p = "MyWorks/a.fit"
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, name string)
		want   string
	}{
		{
			name: "rewritten with a new mtime",
			change: func(t *testing.T, name string) {
				if err := os.WriteFile(name, []byte("xyz"), 0o666); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(name, time.Time{}, deviceMTime.Add(3*time.Second)); err != nil {
					t.Fatal(err)
				}
			},
			want: "after hashing: the local file changed: it has size 3 and mtime 2026-10-29T03:31:22Z",
		},
		{
			// Its bytes were read already, so only the size shows it.
			name: "truncated with its mtime kept",
			change: func(t *testing.T, name string) {
				if err := os.Truncate(name, 0); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(name, time.Time{}, deviceMTime); err != nil {
					t.Fatal(err)
				}
			},
			want: "after hashing: the local file changed: it has size 0 and mtime 2026-10-29T03:31:19Z",
		},
		{
			name: "replaced by another file",
			change: func(t *testing.T, name string) {
				if err := os.Rename(name, name+".old"); err != nil {
					t.Fatal(err)
				}
				writeFile(t, name, "xyz")
				if err := os.Chtimes(name, time.Time{}, deviceMTime); err != nil {
					t.Fatal(err)
				}
			},
			want: "the local path no longer leads to the file that was hashed",
		},
		{
			name: "replaced by a link to it",
			change: func(t *testing.T, name string) {
				if err := os.Rename(name, name+".old"); err != nil {
					t.Fatal(err)
				}
				symlink(t, filepath.Base(name)+".old", name)
			},
			want: "the local path no longer leads to the file that was hashed",
		},
		{
			name: "removed",
			change: func(t *testing.T, name string) {
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			},
			want: "stat the local path after hashing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t, func(dest string) { writeLocal(t, dest, p, "abc") })
			c := fx.copier(t)
			ctx := &hookContext{Context: t.Context(), hook: func() {
				tc.change(t, filepath.Join(fx.dest, filepath.FromSlash(p)))
			}}
			// Run would call Err before the adoption starts.
			d := decision(plan.Adopt, p, 3)
			rec, err := c.adopt(ctx, d)
			fx.checkFailed(t, []Result{{Decision: d, Record: rec, Err: err}}, tc.want)
			if got := fx.records(t); len(got) != 0 {
				t.Errorf("state records: %q", got)
			}
		})
	}
}
