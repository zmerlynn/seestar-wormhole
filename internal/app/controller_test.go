package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/copier"
	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

var oldMTime = time.Date(2026, 10, 29, 3, 31, 19, 0, time.UTC)

func image(data string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(data), ModTime: oldMTime}
}

// copyEnv is a controller copying from a fake share into a temp folder.
type copyEnv struct {
	t        *testing.T
	c        *controller
	files    fstest.MapFS
	set      Settings
	stateDir string
	mu       sync.Mutex
	faults   map[string]remote.Fault
	dialErr  error
	dials    int
}

func newCopyEnv(t *testing.T, files fstest.MapFS) *copyEnv {
	t.Helper()
	e := &copyEnv{t: t, files: files, stateDir: t.TempDir()}
	e.set = defaultSettings()
	e.set.Address, e.set.Folder = "192.0.2.10:445", filepath.Join(t.TempDir(), "Seestar")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.c = newController(t.Context(), e.stateDir, log, func(ctx context.Context, endpoint string) (remote.Session, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.dials++
		if e.dialErr != nil {
			return nil, e.dialErr
		}
		f := remote.NewFake(e.files)
		f.Faults = e.faults
		return f, nil
	})
	t.Cleanup(e.c.quit)
	return e
}

// wait polls the view until its state is one of states.
func (e *copyEnv) wait(states ...string) copyView {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		v := e.c.view(time.Now())
		if slices.Contains(states, v.State) {
			return v
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("state %q, want one of %q: %+v", v.State, states, v)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitOff waits for the loop to return.
func (e *copyEnv) waitOff() {
	e.t.Helper()
	e.c.mu.Lock()
	done := e.c.done
	e.c.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			e.t.Fatal("the loop did not return")
		}
	}
}

// next presses Check now until the next pass has started, since a press
// before the loop's wait is dropped, then waits for that pass to end.
func (e *copyEnv) next() copyView {
	e.t.Helper()
	e.mu.Lock()
	before := e.dials
	e.mu.Unlock()
	passes := func() int {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.dials
	}
	// A press is received only by a wait, so press again only if no pass
	// started for a while.
	deadline := time.Now().Add(10 * time.Second)
	for started := false; !started; {
		if time.Now().After(deadline) {
			e.t.Fatal("Check now started no pass")
		}
		e.c.check()
		for i := 0; i < 50 && !started; i++ {
			time.Sleep(time.Millisecond)
			started = passes() > before
		}
	}
	return e.wait(stUpToDate, stFailures, stUnreachable, stListFail)
}

func (e *copyEnv) locked() bool {
	e.t.Helper()
	st, err := state.Open(e.stateDir, e.set.Folder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if errors.Is(err, state.ErrLocked) {
		return true
	}
	if err != nil {
		e.t.Fatal(err)
	}
	st.Close()
	return false
}

// Start runs a pass, holds the lock while waiting, Check now runs the next
// pass, and Stop while waiting turns copying off and frees the lock.
func TestCopyStartCheckStop(t *testing.T) {
	e := newCopyEnv(t, fstest.MapFS{"MyWorks/M 42/a.jpg": image("abc")})
	if err := e.c.start(e.set); err != nil {
		t.Fatal(err)
	}
	if err := e.c.start(e.set); !errors.Is(err, errBusy) {
		t.Errorf("a second start: got %v, want errBusy", err)
	}
	v := e.wait(stUpToDate)
	if !strings.HasPrefix(v.Title, "Up to date. Next check at ") || v.Chip != "Up to date" || v.Contact != "ok" || !v.On {
		t.Errorf("after a pass: %+v", v)
	}
	if !strings.HasPrefix(v.Summary, "Copied 1 file (3 B) in ") {
		t.Errorf("summary %q", v.Summary)
	}
	if b, err := os.ReadFile(filepath.Join(e.set.Folder, "MyWorks", "M 42", "a.jpg")); err != nil || string(b) != "abc" {
		t.Errorf("copy: %q, %v", b, err)
	}
	if !e.locked() {
		t.Error("the lock is free while copying is on")
	}

	// A new file waits one pass to be seen unchanged.
	e.files["MyWorks/M 42/b.jpg"] = image("b")
	v = e.next()
	if v.Settling != "1 new file is still being written. They'll be copied on a later check." || v.Summary != "No files were copied. 1 is still being written." {
		t.Errorf("settling: %+v", v)
	}
	e.mu.Lock()
	dials := e.dials
	e.mu.Unlock()
	if dials != 2 {
		t.Errorf("got %d passes, want 2", dials)
	}

	e.c.stop()
	e.waitOff()
	v = e.wait(stOff)
	if v.On || v.Chip != "Copying is off" || v.Title != "Copying is off." || !strings.HasPrefix(v.Summary, "No files were copied.") {
		t.Errorf("after Stop: %+v", v)
	}
	if e.locked() {
		t.Error("the lock is held after Stop")
	}
	if err := e.c.start(e.set); err != nil {
		t.Errorf("start after Stop: %v", err)
	}
	e.wait(stUpToDate)
}

// Stop during a download discards it, says so, and leaves no temp file.
func TestCopyStopMidFile(t *testing.T) {
	e := newCopyEnv(t, fstest.MapFS{"MyWorks/a.jpg": image("abc"), "MyWorks/big.fit": image(strings.Repeat("x", 1000))})
	held := make(chan struct{})
	e.faults = map[string]remote.Fault{"MyWorks/big.fit": {After: 10, Stall: sync.OnceFunc(func() { close(held) })}}
	if err := e.c.start(e.set); err != nil {
		t.Fatal(err)
	}
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("big.fit was not held")
	}
	v := e.wait(stCopying)
	if v.Chip != "Copying" || v.Progress == nil || v.Progress.Total != 1003 {
		t.Errorf("copying: %+v", v)
	}
	// Stop once a.jpg is copied and big.fit is held.
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.c.mu.Lock()
		_, held := e.c.st.flight["MyWorks/big.fit"]
		ready := held && e.c.st.finished == 1
		e.c.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a.jpg was not copied while big.fit was held")
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.c.stop()
	if v := e.c.view(time.Now()); v.State != stStopping && v.State != stStopped {
		t.Errorf("right after Stop: %q", v.State)
	}
	e.waitOff()
	v = e.wait(stStopped)
	if v.Title != "Stopped." || v.Text != "1 file was copied. The 1 in progress was discarded and will be copied next time." {
		t.Errorf("stopped: %+v", v)
	}
	filepath.WalkDir(e.set.Folder, func(p string, d fs.DirEntry, err error) error {
		if err == nil && plan.IsTempName(d.Name()) {
			t.Errorf("temp file left: %s", p)
		}
		return nil
	})
	if e.locked() {
		t.Error("the lock is held after Stop")
	}
}

// A destination another copy holds is Folder in use, and copying is off.
func TestCopyFolderInUse(t *testing.T) {
	e := newCopyEnv(t, fstest.MapFS{"MyWorks/a.jpg": image("abc")})
	other, err := state.Open(e.stateDir, e.set.Folder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.c.start(e.set); err != nil {
		t.Fatal(err)
	}
	e.waitOff()
	v := e.wait(stInUse)
	if v.On || !strings.HasPrefix(v.Title, "Another copy is already using this folder") || v.Chip != "Copying is off" {
		t.Errorf("in use: %+v", v)
	}
	if e.dials != 0 {
		t.Errorf("dialed %d times while in use", e.dials)
	}
	other.Close()
	if err := e.c.start(e.set); err != nil {
		t.Fatal(err)
	}
	e.wait(stUpToDate)
}

// A startup error turns copying off and names its cause; only a refused
// write says to choose another folder.
func TestCopyCantWrite(t *testing.T) {
	e := newCopyEnv(t, fstest.MapFS{})
	parent := filepath.Join(t.TempDir(), "file")
	writeFile(t, parent, "x")
	e.set.Folder = filepath.Join(parent, "Seestar")
	if err := e.c.start(e.set); err != nil {
		t.Fatal(err)
	}
	e.waitOff()
	v := e.wait(stCantWrite)
	if !strings.HasPrefix(v.Title, "Couldn't start copying: create destination: ") ||
		v.Text != "Fix the problem, then start copying." || v.On {
		t.Errorf("can't start: %+v", v)
	}
	e.c.finish(fmt.Errorf("create destination: %w", &fs.PathError{Op: "mkdir", Path: e.set.Folder, Err: fs.ErrPermission}))
	v = e.c.view(time.Now())
	if v.Title != "Can't write to "+e.set.Folder+": permission denied." || v.Text != "Choose a writable folder in Settings, then start copying." {
		t.Errorf("can't write: %+v", v)
	}
	// A refused write elsewhere, such as the state's folder, isn't the
	// folder's fault.
	stateErr := fmt.Errorf("open state: %w", &fs.PathError{Op: "open", Path: filepath.Join(e.c.stateDir, "sync", "x.lock"), Err: fs.ErrPermission})
	e.c.finish(stateErr)
	v = e.c.view(time.Now())
	if v.Title != "Couldn't start copying: "+stateErr.Error()+"." || v.Text != "Fix the problem, then start copying." {
		t.Errorf("can't write the state: %+v", v)
	}
}

// A failed connection keeps copying on, and Check now tries again.
func TestCopyUnreachable(t *testing.T) {
	e := newCopyEnv(t, fstest.MapFS{"MyWorks/a.jpg": image("abc")})
	e.dialErr = errors.New("connection refused")
	if err := e.c.start(e.set); err != nil {
		t.Fatal(err)
	}
	v := e.wait(stUnreachable)
	if v.Title != "Can't reach the telescope at 192.0.2.10:445." || v.Chip != "Copying is on" || v.Contact != "err" || !v.On {
		t.Errorf("unreachable: %+v", v)
	}
	e.mu.Lock()
	e.dialErr = nil
	e.mu.Unlock()
	v = e.next()
	if v.Contact != "ok" {
		t.Errorf("contact %q after reaching it", v.Contact)
	}
}

// The folder removed between passes is created again while the lock stays
// held.
func TestCopyRecreatesFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to remove a folder the copier has open, so it can't go missing while copying is on")
	}
	e := newCopyEnv(t, fstest.MapFS{"MyWorks/a.jpg": image("abc")})
	if err := e.c.start(e.set); err != nil {
		t.Fatal(err)
	}
	e.wait(stUpToDate)
	if err := os.RemoveAll(e.set.Folder); err != nil {
		t.Fatal(err)
	}
	e.files["MyWorks/b.jpg"] = image("b")
	if v := e.next(); v.Settling == "" {
		t.Errorf("b.jpg not waiting: %+v", v)
	}
	if !e.locked() {
		t.Error("the lock is free between passes")
	}
	v := e.next()
	if _, err := os.Stat(filepath.Join(e.set.Folder, "MyWorks", "b.jpg")); err != nil || v.Failed != nil {
		t.Errorf("b.jpg after the folder was removed: %v, %+v", err, v)
	}
}

// A listing failure names the folder, a pass with failures lists them, and
// neither is "Up to date".
func TestCopyPassStates(t *testing.T) {
	e := newCopyEnv(t, nil)
	c := e.c
	c.st = status{state: stListing, endpoint: "192.0.2.10:445", contact: "ok", interval: 5 * time.Minute}
	h := c.hooks(t.Context())
	h.PassDone(engine.Summary{Err: fmt.Errorf("list the share: %w", &fs.PathError{Op: "readdir", Path: `MyWorks\M 42_sub`, Err: errors.New("access denied")})})
	v := c.view(time.Now())
	if v.State != stListFail || !strings.HasPrefix(v.Title, "Couldn't read MyWorks/M 42_sub.") || !strings.HasPrefix(v.Text, "No files were copied. Trying again at ") || v.Chip != "Copying is off" {
		t.Errorf("list failed: %+v", v)
	}
	h.PassDone(engine.Summary{Copied: 3, Bytes: 1_200_000, Elapsed: 151 * time.Second, Failed: []engine.Failure{{Path: "a:b.jpg", Reason: "Windows can't store this name (it contains :)"}, {Path: "c.jpg", Reason: "Disk full"}}})
	v = c.view(time.Now())
	if v.State != stFailures || v.Chip != "2 files not copied" || !strings.HasPrefix(v.Title, "Check finished with 2 files not copied. Next check at ") ||
		v.Notice != "2 files couldn't be copied. They'll be tried again at the next check." || len(v.Failed) != 2 || v.Failed[0].Path != "a:b.jpg" ||
		v.Summary != "Copied 3 files (1.2 MB) in 2 min 31 s. 2 failed." {
		t.Errorf("failures: %+v", v)
	}
}

// While a pass lists the share, a line under the title names the folder
// being listed; it goes once the pass has planned.
func TestCopyListingPath(t *testing.T) {
	c := newCopyEnv(t, nil).c
	c.st = status{state: stConnecting}
	h := c.hooks(t.Context())
	h.Listed("MyWorks/M 31", 2)
	if v := c.view(time.Now()); v.State != stListing || v.Title != "Looking for new files… 2 folders checked." || v.Path != "Listing MyWorks/M 31…" {
		t.Errorf("listing: %+v", v)
	}
	h.Planned(0, 0)
	if v := c.view(time.Now()); v.Path != "" {
		t.Errorf("after planning: %+v", v)
	}
}

// The progress view combines the hooks' calls: a fixed total, bytes done
// and in flight, each file's percentage, and speed and time left only
// once they are known. A failed file's bytes leave the count, and the
// speed that then drops below zero is hidden.
func TestCopyProgress(t *testing.T) {
	c := newCopyEnv(t, nil).c
	c.st = status{state: stListing}
	h := c.hooks(t.Context())
	h.Planned(2, 30e6)
	t0 := time.Unix(1000, 0)
	c.st.copyStart, c.st.meter = t0, meter{}
	c.st.meter.add(t0, 0)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	file := func(p string, kind plan.Kind) plan.Decision { return plan.Decision{Kind: kind, Path: p} }

	h.Copying("a.fit", 0, 10e6)
	h.Copying("b.fit", 0, 20e6)
	h.Copying("a.fit", 5e6, 10e6)
	h.Copying("b.fit", 10e6, 20e6)
	c.sample(at(5))
	v := c.view(at(5))
	p := v.Progress
	if v.Title != "Copying 2 of 2 files" || p == nil || p.Done != 15e6 || p.Total != 30e6 || p.Bytes != "15.0 MB of 30.0 MB" ||
		p.Speed != "3.0 MB/s" || p.Left != "" || p.Found != "" ||
		!slices.Equal(p.Flight, []flight{{"a.fit", 50}, {"b.fit", 50}}) {
		t.Errorf("at 5 s: %q %+v", v.Title, p)
	}

	h.Done(copier.Result{Decision: file("a.fit", plan.Copy), Record: state.Record{Size: 10e6}})
	h.Done(copier.Result{Decision: file("c.fit", plan.Adopt)})
	h.Copying("b.fit", 15e6, 20e6)
	c.sample(at(10))
	v = c.view(at(10))
	p = v.Progress
	if v.Title != "Copying 2 of 2 files" || p.Done != 25e6 || p.Total != 30e6 || p.Speed != "2.5 MB/s" || p.Left != "Less than a minute left" ||
		p.Found != "Found already in your folder: 1" || !slices.Equal(p.Flight, []flight{{"b.fit", 75}}) {
		t.Errorf("at 10 s: %q %+v", v.Title, p)
	}

	h.Done(copier.Result{Decision: file("b.fit", plan.Copy), Err: errors.New("the connection was reset")})
	c.sample(at(16))
	v = c.view(at(16))
	p = v.Progress
	if p.Done != 10e6 || p.Speed != "" || p.Left != "" || len(p.Flight) != 0 {
		t.Errorf("after b.fit failed: %+v", p)
	}
}

func TestTimeLeft(t *testing.T) {
	for _, c := range []struct {
		copying   time.Duration
		speed     float64
		remaining int64
		want      string
	}{
		{9 * time.Second, 1e6, 1e9, ""},     // under 10 s of copying
		{10 * time.Second, 99_999, 1e9, ""}, // under 100 KB/s
		{10 * time.Second, 100e3, 5_900_000, "Less than a minute left"},
		{time.Minute, 1e6, 89e6, "About 1 min left"}, // 1.48 min
		{time.Minute, 1e6, 90e6, "About 2 min left"}, // 1.5 min
		{time.Minute, 1e6, 3570e6, "About 1 h left"}, // 59.5 min
		{time.Minute, 1e6, 5430e6, "About 1 h 31 min left"},
	} {
		if got := timeLeft(c.copying, c.speed, c.remaining); got != c.want {
			t.Errorf("timeLeft(%v, %v, %v) = %q, want %q", c.copying, c.speed, c.remaining, got, c.want)
		}
	}
}

// The speed is the average over the last 10 s of samples.
func TestMeter(t *testing.T) {
	var m meter
	t0 := time.Unix(0, 0)
	m.add(t0, 0)
	if _, ok := m.speed(); ok {
		t.Error("a speed from one sample")
	}
	for i := 1; i <= 10; i++ {
		m.add(t0.Add(time.Duration(i)*time.Second), int64(i)*1e6)
	}
	if s, _ := m.speed(); s != 1e6 {
		t.Errorf("10 s at 1 MB/s: %v", s)
	}
	// Five more seconds at 3 MB/s: the window is now 5 s at 1 and 5 s at 3.
	for i := 11; i <= 15; i++ {
		m.add(t0.Add(time.Duration(i)*time.Second), 10e6+int64(i-10)*3e6)
	}
	if s, _ := m.speed(); s != 2e6 {
		t.Errorf("the last 10 s: %v, want 2e6", s)
	}
	if len(m.samples) != 11 {
		t.Errorf("kept %d samples, want 11", len(m.samples))
	}
}

func TestWording(t *testing.T) {
	for n, want := range map[float64]string{0: "0 B", 999: "999 B", 1000: "1.0 KB", 312e6: "312 MB", 1.2e9: "1.2 GB", 99_949: "99.9 KB", 8.1e6: "8.1 MB"} {
		if got := size(n); got != want {
			t.Errorf("size(%v) = %q, want %q", n, got, want)
		}
	}
	for d, want := range map[time.Duration]string{0: "less than a second", 4 * time.Second: "4 s", 151 * time.Second: "2 min 31 s", 2*time.Hour + 5*time.Minute: "2 h 5 min"} {
		if got := elapsed(d); got != want {
			t.Errorf("elapsed(%v) = %q, want %q", d, got, want)
		}
	}
	got := summaryLine(engine.Summary{Copied: 51, Replaced: 1, Adopted: 3, Waiting: 6, Bytes: 1.2e9, Elapsed: 151 * time.Second, Failed: make([]engine.Failure, 2)})
	want := "Copied 52 files (1.2 GB) in 2 min 31 s. 3 were already in your folder. 1 changed on the telescope and was copied again. 6 are still being written. 2 failed."
	if got != want {
		t.Errorf("summary:\n got %q\nwant %q", got, want)
	}
	// Every file failed: none were copied, though there were new files.
	if got, want := summaryLine(engine.Summary{Failed: make([]engine.Failure, 2)}), "No files were copied. 2 failed."; got != want {
		t.Errorf("all failed: got %q, want %q", got, want)
	}
}

// Recent activity keeps the last 200 records at Info and above, newest
// first, with their attributes.
func TestActivity(t *testing.T) {
	a := &activity{}
	var out strings.Builder
	log := slog.New(newTee(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelWarn}), a))
	log.Debug("hidden")
	log.With("endpoint", "192.0.2.10:445").WithGroup("g").Info("pass done", "copied", 3, "dest", "/data/My Seestar")
	log.Warn("copy failed", "path", "a.jpg", "err", "Disk full")
	lines, seq := a.lines()
	if seq != 2 || len(lines) != 2 {
		t.Fatalf("got %d lines, seq %d: %+v", len(lines), seq, lines)
	}
	if lines[0].Text != `copy failed path=a.jpg err="Disk full"` || !lines[0].Warn {
		t.Errorf("newest: %+v", lines[0])
	}
	if lines[1].Text != `pass done endpoint=192.0.2.10:445 g.copied=3 g.dest="/data/My Seestar"` || lines[1].Warn {
		t.Errorf("oldest: %+v", lines[1])
	}
	if strings.Contains(out.String(), "pass done") || !strings.Contains(out.String(), "copy failed") {
		t.Errorf("the next handler got: %q", out.String())
	}
	for i := range 250 {
		log.Info("n", "i", i)
	}
	lines, seq = a.lines()
	if seq != 252 || len(lines) != activitySize || lines[0].Text != "n i=249" || lines[len(lines)-1].Text != "n i=50" {
		t.Errorf("after 250 more: seq %d, %d lines, %q to %q", seq, len(lines), lines[0].Text, lines[len(lines)-1].Text)
	}
}

// The copy API starts, checks and stops copying; Settings can't be saved
// while copying is on.
func TestCopyAPI(t *testing.T) {
	e := newAppEnv(t, "")
	if w := e.post("/app/api/copy", `{"action":"start"}`); w.Code != 409 || !strings.Contains(w.Body.String(), "Couldn't start copying: set the telescope and folder in Settings first.") {
		t.Errorf("start before settings are saved: %d %q", w.Code, w.Body.String())
	}
	input := `{"address":"192.0.2.10","folder":` + quote(filepath.Join(t.TempDir(), "Seestar")) + `,"interval":"5","skip_built_in":true}`
	if w := e.post("/app/api/settings", input); w.Code != 200 {
		t.Fatalf("save: %d %q", w.Code, w.Body.String())
	}
	if w := e.post("/app/api/copy", `{"action":"jump"}`); w.Code != 400 {
		t.Errorf("unknown action: %d", w.Code)
	}
	w := e.post("/app/api/copy", `{"action":"start"}`)
	if v := decode[copyView](t, w); w.Code != 200 || !v.On {
		t.Errorf("start: %d %+v", w.Code, v)
	}
	if w := e.post("/app/api/copy", `{"action":"start"}`); w.Code != 409 {
		t.Errorf("a second start: %d", w.Code)
	}
	if w := e.post("/app/api/settings", input); w.Code != 409 || !strings.Contains(w.Body.String(), "Stop copying to change settings.") {
		t.Errorf("save while on: %d %q", w.Code, w.Body.String())
	}
	status := func() copyView {
		return decode[struct {
			Copy copyView `json:"copy"`
		}](t, e.serve("GET", "/app/api/status", "", tokenHeader, testToken)).Copy
	}
	deadline := time.Now().Add(10 * time.Second)
	// The fake's file has no mtime, so it fails.
	for v := status(); v.State != stFailures; v = status() {
		if time.Now().After(deadline) {
			t.Fatalf("status %+v", v)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if w := e.post("/app/api/copy", `{"action":"check"}`); w.Code != 200 {
		t.Errorf("check: %d", w.Code)
	}
	if w := e.post("/app/api/copy", `{"action":"stop"}`); w.Code != 200 {
		t.Errorf("stop: %d", w.Code)
	}
	e.s.copy.quit()
	if v := status(); v.State != stOff || v.On {
		t.Errorf("after stop: %+v", v)
	}
	got := decode[struct {
		Activity []entry `json:"activity"`
		Seq      int     `json:"activity_seq"`
	}](t, e.serve("GET", "/app/api/status", "", tokenHeader, testToken))
	if len(got.Activity) == 0 || got.Activity[0].Text != "copying stopped" || got.Seq != len(got.Activity) {
		t.Errorf("activity %+v", got)
	}
}

// Run starts copying when Settings say so, and stops it, freeing the lock,
// before it returns.
func TestRunStartsCopying(t *testing.T) {
	dir := t.TempDir()
	path, stateDir, dest := filepath.Join(dir, "app.json"), filepath.Join(dir, "state"), filepath.Join(dir, "Seestar")
	writeFile(t, path, `{"version":1,"address":"192.0.2.10:445","folder":`+quote(dest)+`}`)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dial := func(context.Context, string) (remote.Session, error) {
		return remote.NewFake(fstest.MapFS{"MyWorks/a.jpg": image("abc")}), nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := Run(ctx, Options{SettingsPath: path, StateDir: stateDir, Version: "v", Log: log, Dial: dial}, func(ctx context.Context, _ string, _ func()) error {
		for {
			if _, err := os.Stat(filepath.Join(dest, "MyWorks", "a.jpg")); err == nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := state.Open(stateDir, dest, log)
	if err != nil {
		t.Fatalf("the lock after Run: %v", err)
	}
	st.Close()
}
