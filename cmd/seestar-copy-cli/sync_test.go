package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/remote"
)

// device is the share the fake dial serves. Each dial gets a snapshot of
// files, so a test can change the device between passes.
type device struct {
	mu     sync.Mutex
	files  fstest.MapFS
	faults map[string]remote.Fault
	// unreadable folders fail to list.
	unreadable []string
	// dialErrs fail the first dials, one each.
	dialErrs      []error
	dials, closes int
}

func newDevice() *device { return &device{files: fstest.MapFS{}} }

func (d *device) set(p, data string, mtime time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.files[p] = &fstest.MapFile{Data: []byte(data), ModTime: mtime}
}

// counts returns how many sessions were dialed and how many closed.
func (d *device) counts() (dials, closes int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials, d.closes
}

// use makes dial serve d for the rest of the test.
func (d *device) use(t *testing.T) {
	old := dial
	t.Cleanup(func() { dial = old })
	dial = func(context.Context, string) (remote.Session, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.dials++
		if len(d.dialErrs) > 0 {
			err := d.dialErrs[0]
			d.dialErrs = d.dialErrs[1:]
			return nil, err
		}
		snapshot := fstest.MapFS{}
		for p, f := range d.files {
			snapshot[p] = &fstest.MapFile{Data: f.Data, Mode: f.Mode, ModTime: f.ModTime}
		}
		f := remote.NewFake(unreadableFS{snapshot, d.unreadable})
		f.Faults = d.faults
		return &deviceSession{Fake: f, d: d}, nil
	}
}

// unreadableFS fails ReadDir of the folders in dirs.
type unreadableFS struct {
	fstest.MapFS
	dirs []string
}

func (u unreadableFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if slices.Contains(u.dirs, name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrPermission}
	}
	return u.MapFS.ReadDir(name)
}

// deviceSession counts Close.
type deviceSession struct {
	*remote.Fake
	d *device
}

func (s *deviceSession) Close() error {
	s.d.mu.Lock()
	s.d.closes++
	s.d.mu.Unlock()
	return s.Fake.Close()
}

// lockedBuffer collects the log of a sync running in another goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

// next returns what was logged since the last call.
func (b *lockedBuffer) next() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.b.String()
	b.b.Reset()
	return s
}

// logLines renders the expected log lines at time at, each "level msg rest".
func logLines(at time.Time, lines ...string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("time=" + at.UTC().Format("2006-01-02T15:04:05.000Z") + " level=" + l + "\n")
	}
	return b.String()
}

// place writes a local file under dest with the given mtime.
func place(t *testing.T, dest, p, data string, mtime time.Time) {
	t.Helper()
	name := filepath.Join(dest, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// checkLocal checks the content and mtime of a local file.
func checkLocal(t *testing.T, dest, p, data string, mtime time.Time) {
	t.Helper()
	name := filepath.Join(dest, filepath.FromSlash(p))
	got, err := os.ReadFile(name)
	if err != nil || string(got) != data {
		t.Errorf("%s: got %q, %v; want %q", p, got, err, data)
		return
	}
	fi, err := os.Stat(name)
	if err != nil || !fi.ModTime().Equal(mtime) {
		t.Errorf("%s: mtime %v, %v; want %v", p, fi.ModTime(), err, mtime)
	}
}

// records returns the how of the last state record of each path.
func records(t *testing.T, stateDir string) map[string]string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(stateDir, "sync", "*.jsonl"))
	if err != nil || len(names) != 1 {
		t.Fatalf("state files %q, %v; want one", names, err)
	}
	f, err := os.Open(names[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Scan() // the header
	for sc.Scan() {
		var rec struct{ Path, How string }
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("state line %q: %v", sc.Text(), err)
		}
		got[rec.Path] = rec.How
	}
	return got
}

// checkNoTemp fails if a temporary file is left under dest.
func checkNoTemp(t *testing.T, dest string) {
	t.Helper()
	filepath.WalkDir(dest, func(p string, de fs.DirEntry, err error) error {
		if err == nil && plan.IsTempName(de.Name()) {
			t.Errorf("temporary file left: %s", p)
		}
		return err
	})
}

// Several passes in one process: the first-pass age, the ready age, the
// stability check against the previous pass's listing, adoption, a locally
// deleted file that is not copied again, and a changed device file that is
// replaced. A stop while waiting for the next pass exits 0.
func TestSyncPasses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t0 := time.Now()
		dest, stateDir := t.TempDir(), t.TempDir()
		dev := newDevice()
		dev.use(t)
		const (
			dark    = "Autorun/Dark/dark.fit"
			growing = "MyWorks/M 31/growing.fit"
			have    = "MyWorks/M 31/have.fit"
			old     = "MyWorks/M 31/old.fit"
			settled = "MyWorks/M 31/settled.fit"
			young   = "MyWorks/M 31/young.fit"
		)
		dev.set(dark, "d1", t0.Add(-20*time.Minute))
		dev.set(growing, "g", t0.Add(-3*time.Minute))
		dev.set(have, "hh", t0.Add(-20*time.Minute))
		dev.set(old, "old", t0.Add(-20*time.Minute))
		dev.set(settled, "set", t0.Add(-8*time.Minute))
		dev.set(young, "yo", t0.Add(-time.Minute))
		place(t, dest, have, "hh", t0.Add(-20*time.Minute))

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		logs := &lockedBuffer{}
		code := make(chan int, 1)
		go func() {
			code <- run(ctx, []string{"sync", "--endpoint", "127.0.0.1:4445", "--dest", dest, "--state-dir", stateDir,
				"--interval", "5m", "--ready-age", "7m", "--first-pass-age", "10m", "--workers", "1"}, io.Discard, logs)
		}()

		// Pass 1: only files at least --first-pass-age old are stable.
		synctest.Wait()
		if got, want := logs.next(), logLines(t0,
			`INFO msg="file copied" path=Autorun/Dark/dark.fit size=2`,
			`INFO msg="file adopted" path="MyWorks/M 31/have.fit" size=2`,
			`INFO msg="file copied" path="MyWorks/M 31/old.fit" size=3`,
			`INFO msg="pass done" copied=2 replaced=0 adopted=1 waiting=3 failed=0 skipped=0 unmatched=0 bytes=5 elapsed=0s`,
		); got != want {
			t.Fatalf("pass 1 log:\n%s\nwant:\n%s", got, want)
		}
		if err := os.Remove(filepath.Join(dest, filepath.FromSlash(old))); err != nil {
			t.Fatal(err)
		}
		dev.set(growing, "gg", t0.Add(-3*time.Minute))
		dev.set(dark, "d22", t0.Add(time.Minute))

		// Pass 2: settled is stable now. growing is old enough but its
		// size changed, young and the changed dark file are too new, and
		// old, deleted locally, stays done.
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if got, want := logs.next(), logLines(t0.Add(5*time.Minute),
			`INFO msg="file copied" path="MyWorks/M 31/settled.fit" size=3`,
			`INFO msg="pass done" copied=1 replaced=0 adopted=0 waiting=3 failed=0 skipped=0 unmatched=0 bytes=3 elapsed=0s`,
		); got != want {
			t.Fatalf("pass 2 log:\n%s\nwant:\n%s", got, want)
		}

		// Pass 3: everything left is stable and old enough.
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if got, want := logs.next(), logLines(t0.Add(10*time.Minute),
			`INFO msg="file replaced" path=Autorun/Dark/dark.fit size=3`,
			`INFO msg="file copied" path="MyWorks/M 31/growing.fit" size=2`,
			`INFO msg="file copied" path="MyWorks/M 31/young.fit" size=2`,
			`INFO msg="pass done" copied=2 replaced=1 adopted=0 waiting=0 failed=0 skipped=0 unmatched=0 bytes=7 elapsed=0s`,
		); got != want {
			t.Fatalf("pass 3 log:\n%s\nwant:\n%s", got, want)
		}

		cancel()
		if c := <-code; c != exitOK {
			t.Errorf("exit %d after a stop, want 0", c)
		}
		if got := logs.next(); got != "" {
			t.Errorf("log after the stop: %q, want nothing", got)
		}
		if dials, closes := dev.counts(); dials != 3 || closes != 3 {
			t.Errorf("%d sessions dialed and %d closed, want 3 and 3", dials, closes)
		}
		checkLocal(t, dest, dark, "d22", t0.Add(time.Minute))
		checkLocal(t, dest, growing, "gg", t0.Add(-3*time.Minute))
		checkLocal(t, dest, have, "hh", t0.Add(-20*time.Minute))
		checkLocal(t, dest, settled, "set", t0.Add(-8*time.Minute))
		checkLocal(t, dest, young, "yo", t0.Add(-time.Minute))
		if _, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(old))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s was copied again after a local delete: %v", old, err)
		}
		want := map[string]string{dark: "replaced", growing: "copied", have: "adopted", old: "copied", settled: "copied", young: "copied"}
		if got := records(t, stateDir); !maps.Equal(got, want) {
			t.Errorf("state %v, want %v", got, want)
		}
	})
}

// --once exits 0 after a pass with no failed files, and 1 after one with a
// failed file, whether the plan or the copy failed it, or a failed listing.
func TestSyncOnce(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name   string
		files  []string
		local  []string // placed in dest first, with other content
		faults map[string]remote.Fault
		// unreadable folders fail to list.
		unreadable []string
		code       int
		copied     []string
		log        []string
	}{
		{name: "clean", files: []string{"MyWorks/a.fit", "MyWorks/b.fit"}, code: exitOK,
			copied: []string{"MyWorks/a.fit", "MyWorks/b.fit"},
			log:    []string{`msg="pass done" copied=2 replaced=0 adopted=0 waiting=0 failed=0 skipped=0 unmatched=0 bytes=6 `}},
		{name: "overwrite", files: []string{"MyWorks/a.fit", "MyWorks/b.fit"}, local: []string{"MyWorks/a.fit"}, code: exitOK,
			copied: []string{"MyWorks/a.fit", "MyWorks/b.fit"},
			log: []string{
				`msg="file copied" path=MyWorks/a.fit size=3 overwrote=true` + "\n",
				`msg="file copied" path=MyWorks/b.fit size=3` + "\n",
			}},
		{name: "copy fails", files: []string{"MyWorks/a.fit", "MyWorks/b.fit"},
			faults: map[string]remote.Fault{"MyWorks/a.fit": {OpenErr: errors.New("open refused")}}, code: exitFailure,
			copied: []string{"MyWorks/b.fit"},
			log: []string{
				`level=WARN msg="file failed" path=MyWorks/a.fit reason="open on the device: open refused"`,
				`msg="pass done" copied=1 replaced=0 adopted=0 waiting=0 failed=1 skipped=0 unmatched=0 bytes=3 `,
				`level=ERROR msg="sync failed" error="failed files: 1"`,
			}},
		{name: "plan fails", files: []string{"MyWorks/a.fit", "MyWorks/A.fit", "MyWorks/b.fit"}, code: exitFailure,
			copied: []string{"MyWorks/b.fit"},
			log: []string{
				`level=WARN msg="file failed" path=MyWorks/A.fit reason=`,
				`level=WARN msg="file failed" path=MyWorks/a.fit reason=`,
				`msg="pass done" copied=1 replaced=0 adopted=0 waiting=0 failed=2 skipped=0 unmatched=0 bytes=3 `,
				`level=ERROR msg="sync failed" error="failed files: 2"`,
			}},
		{name: "listing fails", files: []string{"MyWorks/sub/a.fit", "Autorun/Dark/a.fit"}, unreadable: []string{"MyWorks/sub"}, code: exitFailure,
			log: []string{
				`level=ERROR msg="pass failed" copied=0 replaced=0 adopted=0 waiting=0 failed=0 skipped=0 unmatched=0 bytes=0 elapsed=`,
				` error="list the share: readdir MyWorks/sub: permission denied"` + "\n",
				`level=ERROR msg="sync failed" error="list the share: readdir MyWorks/sub: permission denied"`,
			}},
		{name: "root listing fails", files: []string{"MyWorks/a.fit"}, unreadable: []string{"."}, code: exitFailure,
			log: []string{
				`level=ERROR msg="sync failed" error="list the share: readdir .: permission denied"`,
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest, stateDir := t.TempDir(), t.TempDir()
			dev := newDevice()
			for _, p := range tc.files {
				dev.set(p, "abc", old)
			}
			for _, p := range tc.local {
				place(t, dest, p, "local", old)
			}
			dev.faults = tc.faults
			dev.unreadable = tc.unreadable
			dev.use(t)
			code, stdout, stderr := invoke(t, "sync", "--endpoint", "h:445", "--dest", dest, "--state-dir", stateDir, "--once")
			if code != tc.code || stdout != "" {
				t.Errorf("code=%d stdout=%q, want %d and no stdout", code, stdout, tc.code)
			}
			for _, want := range tc.log {
				if !strings.Contains(stderr, want) {
					t.Errorf("log lacks %q:\n%s", want, stderr)
				}
			}
			if tc.code == exitOK && strings.Contains(stderr, "level=ERROR") {
				t.Errorf("error logged on a clean pass:\n%s", stderr)
			}
			for _, p := range tc.files {
				got, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(p)))
				if copied := slices.Contains(tc.copied, p); copied != (string(got) == "abc") {
					t.Errorf("%s: local %q, %v; want copied %t", p, got, err, copied)
				}
			}
			if dials, closes := dev.counts(); dials != 1 || closes != 1 {
				t.Errorf("%d sessions dialed and %d closed, want 1 and 1", dials, closes)
			}
		})
	}
}

// A failed pass is logged and retried after the interval; it does not stop
// sync.
func TestSyncRetriesFailedPass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t0 := time.Now()
		dest, stateDir := t.TempDir(), t.TempDir()
		dev := newDevice()
		dev.set("MyWorks/a.fit", "abc", t0.Add(-time.Hour))
		dev.dialErrs = []error{errors.New("connection refused")}
		dev.use(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		logs := &lockedBuffer{}
		code := make(chan int, 1)
		go func() {
			code <- run(ctx, []string{"sync", "--endpoint", "h:445", "--dest", dest, "--state-dir", stateDir, "--interval", "1m"}, io.Discard, logs)
		}()
		synctest.Wait()
		if got, want := logs.next(), logLines(t0, `ERROR msg="pass failed" copied=0 replaced=0 adopted=0 waiting=0 failed=0 skipped=0 unmatched=0 bytes=0 elapsed=0s error="connect to h:445: connection refused"`); got != want {
			t.Fatalf("first pass log %q, want %q", got, want)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := logs.next(); !strings.Contains(got, `msg="file copied" path=MyWorks/a.fit size=3`) {
			t.Fatalf("second pass log %q, want the file copied", got)
		}
		cancel()
		if c := <-code; c != exitOK {
			t.Errorf("exit %d after a stop, want 0", c)
		}
	})
}

// A stop before the listing ends still logs the pass's summary, and exits 0.
func TestSyncStopWhileListing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t0 := time.Now()
		dev := newDevice()
		dev.set("MyWorks/a.fit", "abc", t0.Add(-time.Hour))
		dev.use(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var stderr bytes.Buffer
		code := run(ctx, []string{"sync", "--endpoint", "h:445", "--dest", t.TempDir(), "--state-dir", t.TempDir(), "--once"}, io.Discard, &stderr)
		if code != exitOK {
			t.Errorf("exit %d after a stop, want 0", code)
		}
		if got, want := stderr.String(), logLines(t0, `INFO msg="pass stopped" copied=0 replaced=0 adopted=0 waiting=0 failed=0 skipped=0 unmatched=0 bytes=0 elapsed=0s`); got != want {
			t.Errorf("log %q, want %q", got, want)
		}
	})
}

// Each file is logged when it finishes, while another is still being read,
// and a stop then fails the unfinished file without a line for it.
func TestSyncLogsEachFileAsItFinishes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t0 := time.Now()
		dev := newDevice()
		dev.set("MyWorks/a.fit", "abc", t0.Add(-time.Hour))
		dev.set("MyWorks/b.fit", "abc", t0.Add(-time.Hour))
		dev.faults = map[string]remote.Fault{"MyWorks/b.fit": {After: 1, Stall: func() {}}}
		dev.use(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		logs := &lockedBuffer{}
		code := make(chan int, 1)
		go func() {
			code <- run(ctx, []string{"sync", "--endpoint", "h:445", "--dest", t.TempDir(), "--state-dir", t.TempDir(),
				"--workers", "2", "--once"}, io.Discard, logs)
		}()
		synctest.Wait() // a.fit is done and b.fit is stalled mid-file
		if got, want := logs.next(), logLines(t0, `INFO msg="file copied" path=MyWorks/a.fit size=3`); got != want {
			t.Fatalf("log while b.fit is read %q, want %q", got, want)
		}
		cancel()
		if c := <-code; c != exitOK {
			t.Errorf("exit %d after a stop, want 0", c)
		}
		if got, want := logs.next(), logLines(t0, `INFO msg="pass stopped" copied=1 replaced=0 adopted=0 waiting=0 failed=1 skipped=0 unmatched=0 bytes=3 elapsed=0s`); got != want {
			t.Errorf("log after the stop %q, want %q", got, want)
		}
	})
}

// A stop mid-file cancels the read, removes the temporary file, records
// nothing for it, closes the session and exits 0, even with --once.
func TestSyncStopMidFile(t *testing.T) {
	dest, stateDir := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dev := newDevice()
	old := time.Now().Add(-time.Hour)
	dev.set("MyWorks/a.fit", "abc", old)
	dev.set("MyWorks/b.fit", "abc", old)
	dev.faults = map[string]remote.Fault{"MyWorks/a.fit": {After: 1, Stall: cancel}}
	dev.use(t)
	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"sync", "--endpoint", "h:445", "--dest", dest, "--state-dir", stateDir, "--workers", "1", "--once"}, &stdout, &stderr)
	if code != exitOK {
		t.Errorf("exit %d after a stop, want 0\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), `msg="pass stopped" copied=0 replaced=0 adopted=0 waiting=0 failed=2 skipped=0 unmatched=0 bytes=0 `) ||
		strings.Contains(stderr.String(), "level=WARN") || strings.Contains(stderr.String(), "level=ERROR") {
		t.Errorf("log after a stop:\n%s\nwant only the stopped pass's summary", stderr.String())
	}
	for _, p := range []string{"MyWorks/a.fit", "MyWorks/b.fit"} {
		if _, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(p))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s exists after a stop: %v", p, err)
		}
	}
	checkNoTemp(t, dest)
	if got := records(t, stateDir); len(got) != 0 {
		t.Errorf("state records after a stop: %v", got)
	}
	if dials, closes := dev.counts(); dials != 1 || closes != 1 {
		t.Errorf("%d sessions dialed and %d closed, want 1 and 1", dials, closes)
	}
}

// pendingSession models a request a silent server never answers: canceling
// the Open context alone does not end it; canceling Dial's context or
// closing the session does.
type pendingSession struct {
	remote.Session
	lifetime context.Context
	entered  chan struct{}
	closed   chan struct{}
	rescue   chan struct{}
	once     sync.Once
}

func (s *pendingSession) Open(context.Context, string) (remote.File, error) {
	close(s.entered)
	select {
	case <-s.lifetime.Done():
		return nil, s.lifetime.Err()
	case <-s.closed:
		return nil, fs.ErrClosed
	case <-s.rescue:
		return nil, errors.New("rescued by the test")
	}
}

func (s *pendingSession) Close() error {
	s.once.Do(func() { close(s.closed) })
	return s.Session.Close()
}

// A stop ends a pending remote call through the session's lifetime, so dial
// must get the command's context, not a detached one.
func TestSyncStopEndsSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		sess := &pendingSession{
			Session: remote.NewFake(fstest.MapFS{"MyWorks/a.fit": {Data: []byte("abc"), ModTime: time.Now().Add(-time.Hour)}}),
			entered: make(chan struct{}), closed: make(chan struct{}), rescue: make(chan struct{}),
		}
		old := dial
		t.Cleanup(func() { dial = old })
		dial = func(lifetime context.Context, _ string) (remote.Session, error) {
			sess.lifetime = lifetime
			return sess, nil
		}
		dest, stateDir := t.TempDir(), t.TempDir()
		code := make(chan int, 1)
		go func() {
			code <- run(ctx, []string{"sync", "--endpoint", "h:445", "--dest", dest, "--state-dir", stateDir, "--once"}, io.Discard, io.Discard)
		}()
		<-sess.entered
		cancel()
		synctest.Wait()
		var c int
		select {
		case c = <-code:
		default:
			t.Error("sync still blocked after a stop: dial's context was not canceled")
			close(sess.rescue)
			c = <-code
		}
		if c != exitOK {
			t.Errorf("exit %d after a stop, want 0", c)
		}
		select {
		case <-sess.closed:
		default:
			t.Error("session not closed")
		}
		checkNoTemp(t, dest)
		if got := records(t, stateDir); len(got) != 0 {
			t.Errorf("state records after a stop: %v", got)
		}
	})
}
