package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/copier"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
	"github.com/zmerlynn/seestar-wormhole/internal/filelock"
)

// The helper process runs a sync loop into the folder in
// lockHelperDestEnv, with its history in lockHelperStateEnv, from a fake
// share with MyWorks/a.jpg. Once the file go exists in lockHelperCtlEnv,
// the share also has MyWorks/b.jpg, whose download stalls after its first
// byte, leaving its temporary file, until the process is killed.
//
// The stalled read blocks reading the helper's standard input, a pipe the
// test holds open. Blocked on a channel instead, the helper would have no
// goroutine that can wake it, and the runtime would end it with "all
// goroutines are asleep", releasing the folder lock mid-test. A cgo
// build's extra thread hides that from the runtime, so it showed only
// where cgo is off, as on the Windows and macOS runners.
const (
	lockHelperDestEnv  = "SEESTAR_COPY_FOLDER_LOCK_DEST"
	lockHelperStateEnv = "SEESTAR_COPY_FOLDER_LOCK_STATE"
	lockHelperCtlEnv   = "SEESTAR_COPY_FOLDER_LOCK_CTL"
)

func TestFolderLockHelper(t *testing.T) {
	dest := os.Getenv(lockHelperDestEnv)
	if dest == "" {
		t.Skip("run only as a helper process")
	}
	ctl := os.Getenv(lockHelperCtlEnv)
	o := SyncOptions{
		Endpoint: "device:445", Dest: dest, StateDir: os.Getenv(lockHelperStateEnv),
		Interval: 50 * time.Millisecond, Workers: 1,
		Dial: func(context.Context, string) (remote.Session, error) {
			files := fstest.MapFS{"MyWorks/a.jpg": file("abc")}
			f := remote.NewFake(files)
			if _, err := os.Stat(filepath.Join(ctl, "go")); err == nil {
				files["MyWorks/b.jpg"] = file("bbbb")
				f.Faults = map[string]remote.Fault{"MyWorks/b.jpg": {After: 1, Stall: stallOnStdin}}
			}
			return f, nil
		},
	}
	err := RunSync(context.Background(), o, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	t.Fatalf("the helper's sync returned: %v", err)
}

// stallOnStdin blocks until the helper's standard input ends, which is
// when the test that started it has gone, and then exits.
func stallOnStdin() {
	io.Copy(io.Discard, os.Stdin)
	os.Exit(1)
}

// startLockHelper starts the helper process on dest with its history in
// stateDir, controlled by ctl, and kills it when the test ends. The
// returned channel is closed if the helper exits first.
func startLockHelper(t *testing.T, dest, stateDir, ctl string) <-chan struct{} {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFolderLockHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), lockHelperDestEnv+"="+dest, lockHelperStateEnv+"="+stateDir, lockHelperCtlEnv+"="+ctl)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		stdin.Close()
		<-exited
		if t.Failed() {
			t.Logf("helper output:\n%s", out.String())
		}
	})
	return exited
}

// helperAlive fails the test if the helper has exited, which would have
// released its folder lock.
func helperAlive(t *testing.T, exited <-chan struct{}) {
	t.Helper()
	select {
	case <-exited:
		t.Fatal("the helper process exited while it should hold the folder lock")
	default:
	}
}

// waitFor polls until ok reports true, failing the test after 30s.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// exists reports whether name exists.
func exists(name string) bool {
	_, err := os.Lstat(name)
	return err == nil
}

// helperTemp is the temporary file of the helper's stalled download in
// dest.
func helperTemp(dest string) string {
	return filepath.Join(dest, "MyWorks", plan.TempName("MyWorks/b.jpg"))
}

// refusedSecondSync runs one sync pass into dest, with another history
// folder, and checks that the folder lock refuses it before it dials, that
// tmp survives, and that its history lock was released again. The helper
// must still be running before and after.
func refusedSecondSync(t *testing.T, dest, tmp string, helper <-chan struct{}) {
	t.Helper()
	helperAlive(t, helper)
	defer helperAlive(t, helper)
	o := syncOptions(t, fstest.MapFS{"MyWorks/a.jpg": file("abc")}, nil)
	o.Dest = dest
	dials := 0
	dial := o.Dial
	o.Dial = func(ctx context.Context, ep string) (remote.Session, error) { dials++; return dial(ctx, ep) }
	err := RunSync(t.Context(), o, discard)
	if !errors.Is(err, state.ErrFolderLocked) || !strings.Contains(err.Error(), plan.LockName) || dials != 0 {
		t.Errorf("second sync: got %v after %d dials, want the folder-lock error naming %s and no dial", err, dials, plan.LockName)
	}
	if !exists(tmp) {
		t.Errorf("the first sync's temporary file %s is gone", tmp)
	}
	st, err := state.Open(o.StateDir, dest, discard)
	if err != nil {
		t.Errorf("the refused sync kept its history lock: %v", err)
	} else {
		st.Close()
	}
}

// Two processes with different history folders cannot sync into one
// folder at once, however it is spelled: the second is refused, its own
// history lock is released, and the first's temporary file survives.
func TestFolderLockAcrossHistoryRoots(t *testing.T) {
	for _, spelling := range []string{"direct", "symlink"} {
		t.Run(spelling, func(t *testing.T) {
			base := t.TempDir()
			dest, ctl := filepath.Join(base, "dest"), filepath.Join(base, "ctl")
			if err := os.Mkdir(ctl, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(ctl, "go"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			second := dest
			if spelling == "symlink" {
				second = filepath.Join(base, "link")
				if err := os.Symlink(dest, second); err != nil {
					t.Skipf("no symbolic links here: %v", err)
				}
			}
			helper := startLockHelper(t, dest, filepath.Join(base, "r1"), ctl)
			tmp := helperTemp(dest)
			waitFor(t, "the helper's temporary file", func() bool { return exists(tmp) })
			refusedSecondSync(t, second, tmp, helper)
		})
	}
}

// A sync pass writes through the folder it opened, so the lock follows a
// renamed folder: after the folder is renamed and replaced between
// passes, the helper's next pass writes and locks the renamed one, and a
// second sync there is refused.
func TestFolderLockFollowsRename(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to rename a folder the copier has open")
	}
	base := t.TempDir()
	dest, moved, ctl := filepath.Join(base, "F"), filepath.Join(base, "F2"), filepath.Join(base, "ctl")
	if err := os.Mkdir(ctl, 0o755); err != nil {
		t.Fatal(err)
	}
	helper := startLockHelper(t, dest, filepath.Join(base, "r1"), ctl)
	waitFor(t, "the helper's first copy", func() bool { return exists(filepath.Join(dest, "MyWorks", "a.jpg")) })
	if err := os.Rename(dest, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctl, "go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := helperTemp(moved)
	waitFor(t, "the helper's temporary file in the renamed folder", func() bool { return exists(tmp) })
	refusedSecondSync(t, moved, tmp, helper)
	if ents, err := os.ReadDir(dest); err != nil || len(ents) != 0 {
		t.Errorf("the new folder at the old path: %v, %v, want it empty", ents, err)
	}
}

// lockedBy reports the error of taking dest's folder lock now, after
// releasing it again if it was free.
func lockedBy(dest string) error {
	r, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer r.Close()
	l, err := state.LockFolder(r)
	if err == nil {
		l.Close()
	}
	return err
}

// syncedLog is a log buffer that a sync loop and the test share.
type syncedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *syncedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *syncedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// The folder lock is per pass: a delete fails while a pass copies, runs
// between passes, and a later pass that finds the lock held is skipped
// with a warning, and the next one runs.
func TestFolderLockBetweenPasses(t *testing.T) {
	s := newShare(t)
	put(t, s.dir, "MyWorks/a.jpg", "abc")
	check := make(chan struct{})
	sums := make(chan Summary)
	entered, release := make(chan struct{}), make(chan struct{})
	logs := &syncedLog{}
	o := SyncOptions{
		Endpoint: "device:445", Dest: s.dest, StateDir: s.stateDir, Interval: time.Hour, Workers: 1, CheckNow: check,
		Dial: func(context.Context, string) (remote.Session, error) { return remote.NewFakeDir(s.dir) },
	}
	o.Hooks.Copying = func(_ string, done, _ int64) {
		if done == 0 {
			entered <- struct{}{}
			<-release
		}
	}
	o.Hooks.PassDone = func(sum Summary) { sums <- sum }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunSync(ctx, o, slog.New(slog.NewTextHandler(logs, nil))) }()
	wait := func(c <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-c:
		case <-time.After(10 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
		}
	}
	next := func() Summary {
		t.Helper()
		select {
		case sum := <-sums:
			return sum
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

	wait(entered, "the first pass's download")
	if _, err := RunDelete(t.Context(), s.options(), discard); !errors.Is(err, state.ErrFolderLocked) {
		t.Errorf("a delete during a pass: got %v, want ErrFolderLocked", err)
	}
	release <- struct{}{}
	if sum := next(); sum.Copied != 1 || sum.Err != nil {
		t.Fatalf("first pass: got %+v, want 1 copied", sum)
	}
	r, err := RunDelete(t.Context(), s.options(), discard)
	if got := outcomes(r); err != nil || !slices.Equal(got, []string{"MyWorks/a.jpg deleted"}) {
		t.Errorf("a delete between passes: got %q, %v, want a.jpg deleted", got, err)
	}

	root, err := os.OpenRoot(s.dest)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	held, err := state.LockFolder(root)
	if err != nil {
		t.Fatal(err)
	}
	press()
	if sum := next(); !errors.Is(sum.Err, state.ErrFolderLocked) {
		t.Errorf("a pass while another writer holds the folder: got %+v, want ErrFolderLocked", sum)
	}
	if lines := logs.String(); !strings.Contains(lines, `level=WARN msg="pass skipped" reason=folder_busy`) {
		t.Errorf("no pass skipped warning in:\n%s", lines)
	}
	held.Close()
	press()
	if sum := next(); sum.Err != nil {
		t.Errorf("the pass after the lock is free: got %+v, want it run", sum)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("got %v after a stop, want nil", err)
	}
}

// The folder lock is held from before the sweep for leftover temporary
// files through the downloads, and released before PassDone.
func TestFolderLockLifetime(t *testing.T) {
	o := syncOptions(t, fstest.MapFS{"MyWorks/a.jpg": file("abc")}, nil)
	sweeps := 0
	orig := sweep
	t.Cleanup(func() { sweep = orig })
	sweep = func(c *copier.Copier) error {
		sweeps++
		if err := lockedBy(o.Dest); !errors.Is(err, state.ErrFolderLocked) {
			t.Errorf("the sweep: got %v taking the folder lock, want ErrFolderLocked", err)
		}
		return orig(c)
	}
	o.Hooks.Done = func(copier.Result) {
		if err := lockedBy(o.Dest); !errors.Is(err, state.ErrFolderLocked) {
			t.Errorf("a download: got %v taking the folder lock, want ErrFolderLocked", err)
		}
	}
	o.Hooks.PassDone = func(Summary) {
		if err := lockedBy(o.Dest); err != nil {
			t.Errorf("after the pass: got %v taking the folder lock, want it free", err)
		}
	}
	if err := RunSync(t.Context(), o, discard); err != nil || sweeps != 1 {
		t.Errorf("got %v after %d sweeps, want nil after 1", err, sweeps)
	}
}

// An old seestar-copy-cli holds only the history lock, <sha256>.lock, in
// its history folder. A sync with the same history folder still stops on
// it; one with another history folder does not, which is the documented
// limit.
func TestFolderLockOldHolder(t *testing.T) {
	o := syncOptions(t, fstest.MapFS{"MyWorks/a.jpg": file("abc")}, nil)
	abs, err := filepath.Abs(o.Dest)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(abs))
	name := filepath.Join(o.StateDir, "sync", hex.EncodeToString(sum[:])+".lock")
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if ok, err := filelock.TryLock(f); !ok || err != nil {
		t.Fatalf("lock %s: %v, %v", name, ok, err)
	}
	if err := RunSync(t.Context(), o, discard); !errors.Is(err, state.ErrLocked) {
		t.Errorf("with the old holder's history folder: got %v, want ErrLocked", err)
	}
	o.StateDir = filepath.Join(t.TempDir(), "other")
	if err := RunSync(t.Context(), o, discard); err != nil || !exists(filepath.Join(o.Dest, "MyWorks", "a.jpg")) {
		t.Errorf("with another history folder: got %v, want a.jpg copied", err)
	}
}

// countDials counts the sessions a run opens.
func countDials(dial func(context.Context, string) (remote.Session, error), n *int) func(context.Context, string) (remote.Session, error) {
	return func(ctx context.Context, ep string) (remote.Session, error) {
		*n++
		return dial(ctx, ep)
	}
}

// recordedShare is a share with MyWorks/a.jpg copied and recorded.
func recordedShare(t *testing.T) *share {
	s := newShare(t)
	put(t, s.dir, "MyWorks/a.jpg", "abc")
	put(t, s.dest, "MyWorks/a.jpg", "abc")
	s.record(t, map[string]string{"MyWorks/a.jpg": "abc"})
	return s
}

// syncFrom returns the options of one sync pass from s.
func (s *share) syncFrom() SyncOptions {
	return SyncOptions{
		Endpoint: "device:445", Dest: s.dest, StateDir: s.stateDir, Interval: time.Hour, Once: true, Workers: 1,
		Dial: func(context.Context, string) (remote.Session, error) { return remote.NewFakeDir(s.dir) },
	}
}

// When the lock file cannot be created, sync and delete fail before they
// connect, except that a delete on a read-only file system proceeds.
// Without a folder, a delete takes no lock.
func TestFolderLockCantCreate(t *testing.T) {
	t.Run("not writable", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs a folder this user cannot write")
		}
		s := recordedShare(t)
		if err := os.Chmod(s.dest, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(s.dest, 0o755) })
		dials := 0
		o := s.syncFrom()
		o.Dial = countDials(o.Dial, &dials)
		if err := RunSync(t.Context(), o, discard); err == nil || !strings.Contains(err.Error(), plan.LockName) || dials != 0 {
			t.Errorf("sync: got %v after %d dials, want an error naming the lock file and no dial", err, dials)
		}
		for _, trust := range []bool{false, true} {
			d := s.options()
			d.TrustState = trust
			d.Dial = countDials(d.Dial, &dials)
			if _, err := RunDelete(t.Context(), d, discard); err == nil || !strings.Contains(err.Error(), plan.LockName) || dials != 0 {
				t.Errorf("delete, trust %v: got %v after %d dials, want an error naming the lock file and no dial", trust, err, dials)
			}
		}
		if !s.has(t, "MyWorks/a.jpg") {
			t.Error("a.jpg was deleted")
		}
	})
	t.Run("read-only file system", func(t *testing.T) {
		orig := lockFolder
		t.Cleanup(func() { lockFolder = orig })
		calls := 0
		lockFolder = func(r *os.Root) (*os.File, error) {
			calls++
			return nil, fmt.Errorf("destination %s: lock file: %w: %w", r.Name(), state.ErrReadOnlyFS, errors.New("read-only file system"))
		}
		s := recordedShare(t)
		if err := RunSync(t.Context(), s.syncFrom(), discard); !errors.Is(err, state.ErrReadOnlyFS) {
			t.Errorf("sync: got %v, want ErrReadOnlyFS", err)
		}
		for _, trust := range []bool{false, true} {
			s := recordedShare(t)
			d := s.options()
			d.TrustState = trust
			var logs bytes.Buffer
			r, err := RunDelete(t.Context(), d, slog.New(slog.NewTextHandler(&logs, nil)))
			if got := outcomes(r); err != nil || !slices.Equal(got, []string{"MyWorks/a.jpg deleted"}) {
				t.Errorf("delete, trust %v: got %q, %v, want a.jpg deleted", trust, got, err)
			}
			if !strings.Contains(logs.String(), `level=WARN msg="deleting without the folder lock" dest=`) {
				t.Errorf("delete, trust %v: no warning in:\n%s", trust, logs.String())
			}
		}
		if calls != 3 {
			t.Errorf("%d lock attempts, want 3", calls)
		}
	})
	t.Run("no folder", func(t *testing.T) {
		orig := lockFolder
		t.Cleanup(func() { lockFolder = orig })
		calls := 0
		lockFolder = func(r *os.Root) (*os.File, error) { calls++; return orig(r) }
		s := recordedShare(t)
		if err := os.RemoveAll(s.dest); err != nil {
			t.Fatal(err)
		}
		d := s.options()
		if _, err := RunDelete(t.Context(), d, discard); !errors.Is(err, ErrNoDest) {
			t.Errorf("hash-proof delete: got %v, want ErrNoDest", err)
		}
		d.TrustState = true
		r, err := RunDelete(t.Context(), d, discard)
		if got := outcomes(r); err != nil || !slices.Equal(got, []string{"MyWorks/a.jpg deleted"}) {
			t.Errorf("trusted delete: got %q, %v, want a.jpg deleted", got, err)
		}
		put(t, s.dir, "MyWorks/b.jpg", "b")
		d = s.options()
		d.IgnoreState, d.Dest, d.StateDir = true, "", ""
		if r, err := RunDelete(t.Context(), d, discard); err != nil || len(r.Files) != 1 {
			t.Errorf("delete without state: got %q, %v, want b.jpg deleted", outcomes(r), err)
		}
		if calls != 0 || exists(s.dest) {
			t.Errorf("%d lock attempts and folder exists %v, want none and no folder", calls, exists(s.dest))
		}
	})
}

// A share entry under the reserved top-level name, in any spelling the
// clash key folds together, is never copied, adopted or recorded, even
// when the selection would keep it, and fails nothing. One warning names
// them. The lock stays effective while the pass copies the rest.
func TestSyncReservedName(t *testing.T) {
	reserved := []string{
		".seestar-wormhole.lock",
		".Seestar-Wormhole.LOCK",
		// A long s folds to s; a Kelvin sign is K after NFC.
		".ſeestar-wormhole.locK",
		".SEESTAR-WORMHOLE.lock/a.fit",
	}
	files := fstest.MapFS{"MyWorks/a.jpg": file("abc"), "MyWorks/.seestar-wormhole.lock": file("deep")}
	for _, p := range reserved {
		files[p] = file("share")
	}
	o := syncOptions(t, files, nil)
	o.Selector = selector(t, nil, []string{"*"})
	adoptable := runtime.GOOS == "linux"
	if adoptable {
		// A local copy that would be adopted if the name were not reserved.
		name := filepath.Join(o.Dest, ".SEESTAR-WORMHOLE.lock", "a.fit")
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("share"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, oldMTime, oldMTime); err != nil {
			t.Fatal(err)
		}
	}
	var sum Summary
	o.Hooks.PassDone = func(s Summary) { sum = s }
	o.Hooks.Done = func(r copier.Result) {
		// On a case-insensitive volume a copy of a variant would have
		// replaced the lock file by now.
		if err := lockedBy(o.Dest); !errors.Is(err, state.ErrFolderLocked) {
			t.Errorf("after %s: got %v taking the folder lock, want ErrFolderLocked", r.Decision.Path, err)
		}
	}
	var logs bytes.Buffer
	if err := RunSync(t.Context(), o, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
		t.Fatal(err)
	}
	if sum.Copied != 2 || sum.Adopted != 0 || len(sum.Failed) != 0 {
		t.Errorf("got %+v, want the two other files copied and nothing adopted or failed", sum)
	}
	recs, err := state.Load(o.StateDir, o.Dest, discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := slices.Sorted(maps.Keys(recs)); !slices.Equal(got, []string{"MyWorks/.seestar-wormhole.lock", "MyWorks/a.jpg"}) {
		t.Errorf("recorded %q, want only the two other files", got)
	}
	if data, err := os.ReadFile(filepath.Join(o.Dest, plan.LockName)); err != nil || len(data) != 0 {
		t.Errorf("the lock file: %q, %v, want it empty", data, err)
	}
	ents, err := os.ReadDir(o.Dest)
	if err != nil {
		t.Fatal(err)
	}
	var top []string
	for _, e := range ents {
		top = append(top, e.Name())
	}
	want := []string{plan.LockName, "MyWorks"}
	if adoptable {
		want = append(want, ".SEESTAR-WORMHOLE.lock")
	}
	slices.Sort(want)
	if !slices.Equal(top, want) {
		t.Errorf("destination holds %q, want %q", top, want)
	}
	if n := strings.Count(logs.String(), `msg="reserved name"`); n != 1 ||
		!regexp.MustCompile(`level=WARN msg="reserved name" path=\S+ entries=4 reason=`).MatchString(logs.String()) {
		t.Errorf("%d reserved name warnings, want one naming 4 entries:\n%s", n, logs.String())
	}
}
