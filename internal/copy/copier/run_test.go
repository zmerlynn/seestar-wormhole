package copier

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// A cancel mid-file stops the read, removes the temporary file and records
// nothing; files not yet started are not started.
func TestCancelMidFile(t *testing.T) {
	fx := newFixture(t, nil)
	c := fx.copier(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sess := device(map[string]string{"MyWorks/a.fit": "abc", "MyWorks/b.fit": "abc"})
	sess.Faults = map[string]remote.Fault{"MyWorks/a.fit": {After: 2, Stall: func() {
		// The first two bytes are in the temporary file, beside the
		// final file, by now.
		got, err := os.ReadFile(filepath.Join(fx.dest, "MyWorks", plan.TempName("MyWorks/a.fit")))
		if err != nil || string(got) != "ab" {
			t.Errorf("temporary file mid-file: got %q, %v; want %q", got, err, "ab")
		}
		cancel()
	}}}
	results := run(c, ctx, sess, 1, []plan.Decision{decision(plan.Copy, "MyWorks/a.fit", 3), decision(plan.Copy, "MyWorks/b.fit", 3)})
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if !errors.Is(r.Err, context.Canceled) {
			t.Errorf("%s: got err %v, want one wrapping context.Canceled", r.Decision.Path, r.Err)
		}
		fx.checkAbsent(t, r.Decision.Path)
	}
	if want := "not started: context canceled"; results[1].Err == nil || results[1].Err.Error() != want {
		t.Errorf("MyWorks/b.fit: got err %v, want %q", results[1].Err, want)
	}
	fx.checkNoTemp(t)
	if got := fx.records(t); len(got) != 0 {
		t.Errorf("state records after a cancel: %q", got)
	}
}

// A session that ends mid-file fails the file the same way.
func TestSessionClosedMidFile(t *testing.T) {
	fx := newFixture(t, nil)
	c := fx.copier(t)
	sess := device(map[string]string{"MyWorks/a.fit": "abc"})
	sess.Faults = map[string]remote.Fault{"MyWorks/a.fit": {After: 1, Stall: func() { sess.Close() }}}
	results := run(c, t.Context(), sess, 1, []plan.Decision{decision(plan.Copy, "MyWorks/a.fit", 3)})
	fx.checkFailed(t, results, "download: stopped after 1 of 3 bytes")
	if !errors.Is(results[0].Err, fs.ErrClosed) {
		t.Errorf("got err %v, want one wrapping fs.ErrClosed", results[0].Err)
	}
	fx.checkAbsent(t, "MyWorks/a.fit")
}

// A cancel with every worker mid-file removes every temporary file.
func TestCancelAllWorkers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fx := newFixture(t, nil)
		c := fx.copier(t)
		files := map[string]string{}
		faults := map[string]remote.Fault{}
		var ds []plan.Decision
		for i := range 5 {
			p := fmt.Sprintf("MyWorks/%d.fit", i)
			files[p] = "abc"
			faults[p] = remote.Fault{After: 1, Stall: func() {}}
			ds = append(ds, decision(plan.Copy, p, 3))
		}
		sess := device(files)
		sess.Faults = faults
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan []Result)
		go func() { done <- run(c, ctx, sess, 3, ds) }()
		synctest.Wait() // every worker is stalled mid-file
		if got := fx.temps(t); len(got) != 3 {
			t.Errorf("temporary files with three workers mid-file: got %q, want 3", got)
		}
		cancel()
		results := <-done
		if len(results) != 5 {
			t.Fatalf("got %d results, want 5", len(results))
		}
		for _, r := range results {
			if !errors.Is(r.Err, context.Canceled) {
				t.Errorf("%s: got err %v, want one wrapping context.Canceled", r.Decision.Path, r.Err)
			}
		}
		fx.checkNoTemp(t)
		if got := fx.records(t); len(got) != 0 {
			t.Errorf("state records after a cancel: %q", got)
		}
	})
}

// gated is a session whose files deliver nothing until release is closed,
// and that counts the files being read at once.
type gated struct {
	remote.Session
	release        chan struct{}
	inFlight, peak atomic.Int32
}

func (g *gated) Open(ctx context.Context, p string) (remote.File, error) {
	f, err := g.Session.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	return &gatedFile{File: f, g: g}, nil
}

type gatedFile struct {
	remote.File
	g *gated
}

func (f *gatedFile) WriteTo(w io.Writer) (int64, error) {
	n := f.g.inFlight.Add(1)
	defer f.g.inFlight.Add(-1)
	for {
		m := f.g.peak.Load()
		if n <= m || f.g.peak.CompareAndSwap(m, n) {
			break
		}
	}
	<-f.g.release
	return f.File.WriteTo(w)
}

// Run reads exactly workers files at once, and every file is copied and
// recorded on its own whole line.
func TestRunWorkers(t *testing.T) {
	for _, workers := range []int{0, 1, 3, 8} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				fx := newFixture(t, nil)
				c := fx.copier(t)
				files := map[string]string{}
				var ds []plan.Decision
				for i := range 6 {
					p := fmt.Sprintf("MyWorks/M %d/%d.fit", i%2, i)
					files[p] = "abc"
					ds = append(ds, decision(plan.Copy, p, 3))
				}
				g := &gated{Session: device(files), release: make(chan struct{})}
				done := make(chan []Result)
				go func() { done <- run(c, t.Context(), g, workers, ds) }()
				synctest.Wait() // every worker is waiting at the gate
				want := int32(min(max(workers, 1), len(ds)))
				if got := g.inFlight.Load(); got != want {
					t.Errorf("files read at once: got %d, want %d", got, want)
				}
				close(g.release)
				results := <-done
				if got := g.peak.Load(); got != want {
					t.Errorf("most files read at once: got %d, want %d", got, want)
				}
				if len(results) != len(ds) {
					t.Fatalf("got %d results, want %d", len(results), len(ds))
				}
				for i, r := range results {
					if r.Err != nil || r.Decision.Path != ds[i].Path || r.Record.Path != ds[i].Path {
						t.Errorf("result %d: got %+v, want %s copied", i, r, ds[i].Path)
					}
					fx.checkFile(t, ds[i].Path, "abc")
				}
				if got := fx.records(t); len(got) != len(ds) {
					t.Errorf("got %d state records, want %d: %q", len(got), len(ds), got)
				}
				for p := range files {
					if _, ok := fx.st.Lookup(p); !ok {
						t.Errorf("%s is not recorded", p)
					}
				}
			})
		})
	}
}

// Two files with one base name in different folders, read at once, each
// get their own temporary file and their own bytes.
func TestRunSameBaseName(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fx := newFixture(t, nil)
		c := fx.copier(t)
		files := map[string]string{"MyWorks/M 31/a.fit": "abc", "MyWorks/M 42/a.fit": "xyz"}
		ds := []plan.Decision{decision(plan.Copy, "MyWorks/M 31/a.fit", 3), decision(plan.Copy, "MyWorks/M 42/a.fit", 3)}
		g := &gated{Session: device(files), release: make(chan struct{})}
		done := make(chan []Result)
		go func() { done <- run(c, t.Context(), g, 2, ds) }()
		synctest.Wait() // both are being read
		want := []string{
			"MyWorks/M 31/" + plan.TempName("MyWorks/M 31/a.fit"),
			"MyWorks/M 42/" + plan.TempName("MyWorks/M 42/a.fit"),
		}
		if got := fx.temps(t); !slices.Equal(got, want) {
			t.Errorf("temporary files mid-read: got %q, want %q", got, want)
		}
		close(g.release)
		for _, r := range <-done {
			if r.Err != nil {
				t.Errorf("%s: %v", r.Decision.Path, r.Err)
			}
		}
		for p, data := range files {
			fx.checkFile(t, p, data)
		}
		fx.checkNoTemp(t)
	})
}
