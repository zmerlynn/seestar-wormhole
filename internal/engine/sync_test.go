package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/copier"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// oldMTime is the device mtime of the test files: old enough to copy.
var oldMTime = time.Date(2026, 10, 29, 3, 31, 19, 0, time.UTC)

var discard = slog.New(slog.DiscardHandler)

// syncOptions returns the options of one pass from a fake device serving
// files, with faults, into a new destination.
func syncOptions(t *testing.T, files fstest.MapFS, faults map[string]remote.Fault) SyncOptions {
	base := t.TempDir()
	return SyncOptions{
		Endpoint: "device:445", Dest: filepath.Join(base, "dest"), StateDir: filepath.Join(base, "state"),
		Interval: time.Hour, Once: true, Workers: 2,
		Dial: func(context.Context, string) (remote.Session, error) {
			f := remote.NewFake(files)
			f.Faults = faults
			return f, nil
		},
	}
}

func file(data string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(data), ModTime: oldMTime}
}

// A pass calls each hook with what it lists, plans, copies and did.
func TestSyncHooks(t *testing.T) {
	o := syncOptions(t, fstest.MapFS{
		"MyWorks/M 42/a.fit": file("abc"),
		"MyWorks/M 42/b.fit": file("abcd"),
		"MyWorks/M 31/c.fit": file("ab"),
	}, nil)
	o.Workers = 1
	var listed []int
	var planned [][2]int64
	var copying, done []string
	var sums []Summary
	o.Hooks = Hooks{
		Listed:  func(n int) { listed = append(listed, n) },
		Planned: func(files int, bytes int64) { planned = append(planned, [2]int64{int64(files), bytes}) },
		Copying: func(p string, n, size int64) {
			switch n {
			case 0:
			case size:
				copying = append(copying, p)
			default:
				t.Errorf("Copying %s: got %d of %d bytes; a fast copy reports only its start and its end", p, n, size)
			}
		},
		Done:     func(r copier.Result) { done = append(done, r.Decision.Path) },
		PassDone: func(s Summary) { sums = append(sums, s) },
	}
	if err := RunSync(t.Context(), o, discard); err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 2, 3}; !slices.Equal(listed, want) {
		t.Errorf("Listed: got %v, want %v", listed, want)
	}
	if want := [][2]int64{{3, 9}}; !slices.Equal(planned, want) {
		t.Errorf("Planned: got %v, want %v", planned, want)
	}
	want := []string{"MyWorks/M 31/c.fit", "MyWorks/M 42/a.fit", "MyWorks/M 42/b.fit"}
	if !slices.Equal(copying, want) {
		t.Errorf("Copying: got %q, want %q", copying, want)
	}
	if !slices.Equal(done, want) {
		t.Errorf("Done: got %q, want %q", done, want)
	}
	if len(sums) != 1 || sums[0].Copied != 3 || sums[0].Bytes != 9 || len(sums[0].Failed) != 0 || sums[0].Err != nil {
		t.Errorf("PassDone: got %+v, want one summary of 3 files copied, 9 bytes", sums)
	}
}

// Summary.Failed names every failed file and why: those the decisions
// refused, then those whose copy failed.
func TestSummaryFailed(t *testing.T) {
	o := syncOptions(t, fstest.MapFS{
		"MyWorks/a.jpg": file("a"),
		"MyWorks/A.jpg": file("A"),
		"MyWorks/b.jpg": file("b"),
		"MyWorks/c.jpg": file("c"),
	}, map[string]remote.Fault{"MyWorks/b.jpg": {OpenErr: errors.New("access denied")}})
	var sum Summary
	o.Hooks.PassDone = func(s Summary) { sum = s }
	if err := RunSync(t.Context(), o, discard); err == nil || err.Error() != "failed files: 3" {
		t.Fatalf("got err %v, want failed files: 3", err)
	}
	var paths []string
	for _, f := range sum.Failed {
		paths = append(paths, f.Path)
	}
	if want := []string{"MyWorks/A.jpg", "MyWorks/a.jpg", "MyWorks/b.jpg"}; !slices.Equal(paths, want) {
		t.Fatalf("Failed paths: got %q, want %q", paths, want)
	}
	for _, f := range sum.Failed[:2] {
		if !strings.Contains(f.Reason, "case") {
			t.Errorf("%s: got reason %q, want a case clash", f.Path, f.Reason)
		}
	}
	if want := "open on the device: access denied"; sum.Failed[2].Reason != want {
		t.Errorf("MyWorks/b.jpg: got reason %q, want %q", sum.Failed[2].Reason, want)
	}
	if sum.Copied != 1 {
		t.Errorf("Copied: got %d, want 1", sum.Copied)
	}
}

// Summary.Failed has every failure, however many: more than the app's
// activity log keeps.
func TestSummaryFailedAll(t *testing.T) {
	const n = 300
	files := fstest.MapFS{}
	faults := map[string]remote.Fault{}
	for i := range n {
		p := fmt.Sprintf("MyWorks/%03d.jpg", i)
		files[p] = file("x")
		faults[p] = remote.Fault{OpenErr: errors.New("access denied")}
	}
	o := syncOptions(t, files, faults)
	var sum Summary
	o.Hooks.PassDone = func(s Summary) { sum = s }
	if err := RunSync(t.Context(), o, discard); err == nil {
		t.Fatal("got no error, want failed files")
	}
	got := map[string]bool{}
	for _, f := range sum.Failed {
		got[f.Path] = true
	}
	if len(sum.Failed) != n || len(got) != n {
		t.Errorf("Failed: got %d entries for %d paths, want %d of each", len(sum.Failed), len(got), n)
	}
}

// A pass that cannot connect still ends with PassDone, carrying the error.
func TestSummaryErr(t *testing.T) {
	o := syncOptions(t, fstest.MapFS{}, nil)
	o.Dial = func(context.Context, string) (remote.Session, error) { return nil, errors.New("no route") }
	var sums []Summary
	o.Hooks.PassDone = func(s Summary) { sums = append(sums, s) }
	err := RunSync(t.Context(), o, discard)
	if want := "connect to device:445: no route"; err == nil || err.Error() != want {
		t.Errorf("got err %v, want %q", err, want)
	}
	if len(sums) != 1 || sums[0].Err == nil || sums[0].Err.Error() != "connect to device:445: no route" {
		t.Errorf("PassDone: got %+v, want one summary with the connect error", sums)
	}
}

// CheckNow ends a wait at once; a press while a pass runs finds no wait
// and is dropped, and the interval still ends a wait.
func TestCheckNow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		check := make(chan struct{})
		o := syncOptions(t, fstest.MapFS{}, nil)
		o.Once, o.CheckNow = false, check
		var passes atomic.Int32
		var taken atomic.Bool
		o.Hooks.PassDone = func(Summary) {
			passes.Add(1)
			select {
			case check <- struct{}{}:
				taken.Store(true)
			default:
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		go func() { done <- RunSync(ctx, o, discard) }()
		synctest.Wait() // the first pass is done, and the wait begun
		if got := passes.Load(); got != 1 {
			t.Errorf("passes before Check now: got %d, want 1", got)
		}
		select {
		case check <- struct{}{}:
		default:
			t.Error("the wait did not take Check now")
		}
		synctest.Wait()
		if got := passes.Load(); got != 2 {
			t.Errorf("passes after Check now: got %d, want 2", got)
		}
		if taken.Load() {
			t.Error("a press during a pass was taken")
		}
		time.Sleep(o.Interval)
		synctest.Wait()
		if got := passes.Load(); got != 3 {
			t.Errorf("passes after the interval: got %d, want 3", got)
		}
		cancel()
		if err := <-done; err != nil {
			t.Errorf("got err %v after a stop, want nil", err)
		}
	})
}

// With Recreate, a destination removed between passes is created again,
// and a later pass copies a new file there, while the state stays open
// and locked. Without it, as in the CLI, the new file fails.
func TestRecreate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to remove a folder the copier has open, so it can't go missing while copying is on")
	}
	for _, recreate := range []bool{true, false} {
		t.Run(fmt.Sprint("recreate=", recreate), func(t *testing.T) {
			check := make(chan struct{})
			files := fstest.MapFS{"MyWorks/a.jpg": file("abc")}
			o := syncOptions(t, files, nil)
			o.Once, o.CheckNow, o.Recreate = false, check, recreate
			sums := make(chan Summary)
			o.Hooks.PassDone = func(s Summary) { sums <- s }
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error)
			go func() { done <- RunSync(ctx, o, discard) }()
			// Each wait is bounded, so a lost pass fails here, not at the
			// package timeout.
			next := func() Summary {
				t.Helper()
				select {
				case s := <-sums:
					return s
				case <-time.After(10 * time.Second):
					t.Fatal("no pass ended")
					return Summary{}
				}
			}
			press := func() {
				t.Helper()
				select {
				case check <- struct{}{}:
				case <-time.After(10 * time.Second):
					t.Fatal("the loop did not take Check now")
				}
			}
			if s := next(); s.Copied != 1 {
				t.Fatalf("first pass: got %+v, want 1 copied", s)
			}
			if err := os.RemoveAll(o.Dest); err != nil {
				t.Fatal(err)
			}
			// A new file waits one pass to be seen unchanged.
			files["MyWorks/b.jpg"] = file("b")
			press()
			if s := next(); s.Waiting != 1 {
				t.Fatalf("second pass: got %+v, want b.jpg waiting", s)
			}
			press()
			s := next()
			if _, err := state.Open(o.StateDir, o.Dest, discard); !errors.Is(err, state.ErrLocked) {
				t.Errorf("a second open during the loop: got %v, want ErrLocked", err)
			}
			_, statErr := os.Stat(filepath.Join(o.Dest, "MyWorks", "b.jpg"))
			if recreate {
				if s.Copied != 1 || len(s.Failed) != 0 || statErr != nil {
					t.Errorf("third pass: got %+v and stat %v, want b.jpg copied", s, statErr)
				}
			} else if len(s.Failed) != 1 || statErr == nil {
				t.Errorf("third pass: got %+v and stat %v, want b.jpg failed and absent", s, statErr)
			}
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("got err %v after a stop, want nil", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the loop did not return after a stop")
			}
		})
	}
}
