package ui

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// trailEnv is a routeEnv whose Mount is the app's, from copyMountOptions,
// so every outcome reaches the server's own event trail. Its telescope
// session, remover, settings save and dialogs run named steps, each of
// which a test can make fail or hold. A recorder sees each outcome once
// the trail has taken it.
type trailEnv struct {
	*routeEnv
	stateDir string

	fmu        sync.Mutex
	faults     map[string]error         // a step's error
	holds      map[string]chan struct{} // a step waits for its channel
	entered    map[string]chan struct{} // a step signals its channel on entry
	reports    []copyapp.Outcome
	holdReport func(copyapp.Outcome) // if set, runs before an outcome reaches the trail
	answer     string                // the dialogs' answer when they don't fail
	removers   int
}

// The steps a trailEnv runs.
const (
	stepDial    = "dial"
	stepWalk    = "walk"
	stepReadDir = "readdir"
	stepRemove  = "remove"
	stepRemover = "remover"
	stepSave    = "save"
	stepFolder  = "choose_folder"
	stepSaveAs  = "save_file"
)

func newTrailEnv(t *testing.T) *trailEnv {
	t.Helper()
	base := t.TempDir()
	r := &routeEnv{t: t, s: testServer(t), shares: filepath.Join(base, "shares"), folder: filepath.Join(base, "Seestar")}
	r.s.controller.log = &eventLog{storage: r.s.storage}
	e := &trailEnv{routeEnv: r, stateDir: filepath.Join(base, "state"),
		faults: map[string]error{}, holds: map[string]chan struct{}{}, entered: map[string]chan struct{}{}}
	savedDial, savedRemover := copyDial, tcpRemover
	t.Cleanup(func() { copyDial, tcpRemover = savedDial, savedRemover })
	copyDial = func(ctx context.Context, tg copyapp.Target) (remote.Session, error) {
		if err := e.step(ctx, stepDial); err != nil {
			return nil, err
		}
		d, err := e.share(tg.Endpoint)
		if err != nil {
			return nil, err
		}
		return &trailSession{FakeDir: d, e: e}, nil
	}
	tcpRemover = func(ctx context.Context, endpoint string, _ *slog.Logger) (remote.Remover, error) {
		e.fmu.Lock()
		e.removers++
		e.fmu.Unlock()
		if err := e.step(ctx, stepRemover); err != nil {
			return nil, err
		}
		d, err := e.share(endpoint)
		if err != nil {
			return nil, err
		}
		return &trailSession{FakeDir: d, e: e}, nil
	}
	test := func(ctx context.Context, endpoint string) ([]remote.Entry, error) {
		if strings.HasPrefix(endpoint, "192.0.2.99:") {
			return nil, errors.New("synthetic unreachable")
		}
		return []remote.Entry{{Path: "MyWorks", Kind: remote.Dir}}, nil
	}
	dialog := func(step string) (string, error) {
		if err := e.step(context.Background(), step); err != nil {
			return "", err
		}
		e.fmu.Lock()
		defer e.fmu.Unlock()
		return e.answer, nil
	}
	native := &copyapp.Native{
		ChooseFolder: func(string) (string, error) { return dialog(stepFolder) },
		SaveFile:     func(string) (string, error) { return dialog(stepSaveAs) },
		Alert:        func(string) {},
	}
	o := copyMountOptions(r.s, copyapp.MountOptions{
		Addr: netip.MustParseAddrPort(r.s.host), StateDir: e.stateDir, Version: "synthetic",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Native: native, Test: test, Reach: reachBy(test),
		Save: func(copyapp.Settings) error { return e.step(context.Background(), stepSave) },
	})
	inner := o.Report
	if inner == nil {
		t.Fatal("copyMountOptions set no Report")
	}
	o.Report = func(out copyapp.Outcome) {
		e.fmu.Lock()
		hold := e.holdReport
		e.fmu.Unlock()
		if hold != nil {
			hold(out)
		}
		inner(out)
		e.fmu.Lock()
		e.reports = append(e.reports, out)
		e.fmu.Unlock()
	}
	r.s.copy = copyapp.NewMount(context.Background(), o)
	t.Cleanup(r.s.copy.Quit)
	return e
}

// trailSession is a share whose walk, listing, reads and removes run
// their steps first.
type trailSession struct {
	*remote.FakeDir
	e *trailEnv
}

func (s *trailSession) Walk(ctx context.Context, skip func(string, bool) bool, kept func(remote.Entry)) ([]remote.Entry, error) {
	if err := s.e.step(ctx, stepWalk); err != nil {
		return nil, err
	}
	return s.FakeDir.Walk(ctx, skip, kept)
}

func (s *trailSession) ReadDir(ctx context.Context, dir string) ([]remote.Entry, error) {
	if err := s.e.step(ctx, stepReadDir); err != nil {
		return nil, err
	}
	return s.FakeDir.ReadDir(ctx, dir)
}

func (s *trailSession) Open(ctx context.Context, p string) (remote.File, error) {
	if err := s.e.step(ctx, "open:"+p); err != nil {
		return nil, err
	}
	return s.FakeDir.Open(ctx, p)
}

func (s *trailSession) RemoveFile(ctx context.Context, en remote.Entry) error {
	if err := s.e.step(ctx, stepRemove); err != nil {
		return err
	}
	return s.FakeDir.RemoveFile(ctx, en)
}

// step runs the step name: it signals the step's entered channel, waits
// for its hold, and returns its fault.
func (e *trailEnv) step(ctx context.Context, name string) error {
	e.fmu.Lock()
	err, hold, in := e.faults[name], e.holds[name], e.entered[name]
	e.fmu.Unlock()
	if in != nil {
		select {
		case in <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

// fail makes the step name fail with err, or succeed again for nil.
func (e *trailEnv) fail(name string, err error) {
	e.fmu.Lock()
	defer e.fmu.Unlock()
	if err == nil {
		delete(e.faults, name)
	} else {
		e.faults[name] = err
	}
}

// hold makes the step name wait, signaling entered on entry, until the
// function it returns is called.
func (e *trailEnv) hold(name string) (entered <-chan struct{}, release func()) {
	in, h := make(chan struct{}, 64), make(chan struct{})
	e.fmu.Lock()
	e.holds[name], e.entered[name] = h, in
	e.fmu.Unlock()
	var once sync.Once
	return in, func() {
		once.Do(func() {
			e.fmu.Lock()
			delete(e.holds, name)
			delete(e.entered, name)
			e.fmu.Unlock()
			close(h)
		})
	}
}

// setAnswer sets the dialogs' answer.
func (e *trailEnv) setAnswer(answer string) {
	e.fmu.Lock()
	defer e.fmu.Unlock()
	e.answer = answer
}

// outcomes is the outcomes of source's op the trail has taken so far.
func (e *trailEnv) outcomes(source, op string) []copyapp.Outcome {
	e.fmu.Lock()
	defer e.fmu.Unlock()
	var out []copyapp.Outcome
	for _, o := range e.reports {
		if o.Source == source && o.Op == op {
			out = append(out, o)
		}
	}
	return out
}

// reported waits until the trail has taken n outcomes of source's op,
// and returns the last.
func (e *trailEnv) reported(source, op string, n int) copyapp.Outcome {
	e.t.Helper()
	var got []copyapp.Outcome
	e.until(fmt.Sprintf("%d %s/%s outcomes", n, source, op), func() bool {
		got = e.outcomes(source, op)
		return len(got) >= n
	})
	return got[len(got)-1]
}

// fileLines is gui.log's file-tool lines, oldest first.
func (e *trailEnv) fileLines() []map[string]any {
	var out []map[string]any
	for _, l := range guiLog(e.t, e.s.storage.root) {
		if l["source"] != diagnostic.SourceConnection {
			out = append(out, l)
		}
	}
	return out
}

// trailStatus is /api/status's trail.
type trailStatus struct {
	Events   []map[string]any `json:"events"`
	Failures []opFailure      `json:"failures"`
}

func (e *trailEnv) status() trailStatus {
	e.t.Helper()
	code, body := e.get("/api/status")
	if code != 200 {
		e.t.Fatalf("status = %d %s", code, body)
	}
	var v trailStatus
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		e.t.Fatalf("status %s: %v", body, err)
	}
	return v
}

// failing is the keys /api/status lists as failing, each as source/op.
func (e *trailEnv) failing() []string {
	var out []string
	for _, f := range e.status().Failures {
		out = append(out, f.Source+"/"+f.Op)
	}
	return out
}

// mustPost posts body to path and fails the test unless it answers want.
func (e *trailEnv) mustPost(path, body string, want int) string {
	e.t.Helper()
	code, reply := e.post(path, body)
	if code != want {
		e.t.Fatalf("POST %s %s = %d %s, want %d", path, body, code, reply, want)
	}
	return reply
}

// file is the share's one file at 192.0.2.10.
const trailFile = "MyWorks/at_192.0.2.10.jpg"

// ready saves this network's route and copy's settings.
func (e *trailEnv) ready() {
	e.t.Helper()
	e.lan("192.0.2.10")
	e.settings()
}

// pass starts copying and waits for its n-th pass outcome.
func (e *trailEnv) pass(n int) copyapp.Outcome {
	e.t.Helper()
	e.mustPost("/copy/api/copy", `{"action":"start"}`, 200)
	return e.reported(diagnostic.SourceCopy, diagnostic.OpPass, n)
}

// stopCopying stops copying and waits for it to end.
func (e *trailEnv) stopCopying() {
	e.t.Helper()
	e.mustPost("/copy/api/copy", `{"action":"stop"}`, 200)
	e.until("copying to stop", func() bool { return !e.s.copy.Busy() })
}

// freePreview runs Free up space's check to a preview and returns its id.
func (e *trailEnv) freePreview(days string) int {
	e.t.Helper()
	n := len(e.outcomes(diagnostic.SourceDelete, diagnostic.OpPreview))
	e.mustPost("/copy/api/delete", `{"action":"find","days":"`+days+`","trust":false}`, 200)
	e.reported(diagnostic.SourceDelete, diagnostic.OpPreview, n+1)
	state, id := e.free()
	if state != "preview" {
		e.t.Fatalf("free state %q, want preview", state)
	}
	return id
}

// confirm confirms the preview id and waits for its outcome.
func (e *trailEnv) confirm(id int) copyapp.Outcome {
	e.t.Helper()
	n := len(e.outcomes(diagnostic.SourceDelete, diagnostic.OpDelete))
	e.mustPost("/copy/api/delete", `{"action":"delete","id":`+strconv.Itoa(id)+`,"folders":["MyWorks"]}`, 200)
	return e.reported(diagnostic.SourceDelete, diagnostic.OpDelete, n+1)
}

// lockHistory holds the copy folder's history lock, as another copier
// would, until the test ends.
func (e *trailEnv) lockHistory() {
	e.t.Helper()
	st, err := state.Open(e.stateDir, e.folder, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { st.Close() })
}

// lockFolder holds the copy folder's own lock, as a delete or another
// copier would, and returns its release.
func (e *trailEnv) lockFolder() func() {
	e.t.Helper()
	if err := os.MkdirAll(e.folder, 0o755); err != nil {
		e.t.Fatal(err)
	}
	root, err := os.OpenRoot(e.folder)
	if err != nil {
		e.t.Fatal(err)
	}
	lock, err := state.LockFolder(root)
	if err != nil {
		root.Close()
		e.t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { lock.Close(); root.Close() }) }
	e.t.Cleanup(release)
	return release
}

// browse lists the share's MyWorks and returns the code.
func (e *trailEnv) browse() int {
	code, _ := e.get("/browse/api/list?path=MyWorks")
	return code
}

// errSynthetic is a fault's error: it classifies as operation_failed.
var errSynthetic = errors.New("synthetic failure")

// Each row of the boundary table writes exactly one failed line, with its
// source, op, code, classified phase and reason, and a count where the
// operation has failed files. The rows run through the Mount's HTTP
// handlers and the telescope fakes only.
func TestFileToolOutcomesReachGUILog(t *testing.T) {
	type want struct {
		source, op, phase, reason string
		count                     int
	}
	for _, tc := range []struct {
		name string
		run  func(e *trailEnv)
		want want
	}{
		{"start not connected", func(e *trailEnv) {
			e.settings()
			e.mustPost("/copy/api/copy", `{"action":"start"}`, 409)
		}, want{"copy", "start", "start", "operation_failed", 0}},
		{"open history locked", func(e *trailEnv) {
			e.ready()
			e.lockHistory()
			e.mustPost("/copy/api/copy", `{"action":"start"}`, 200)
			e.reported(diagnostic.SourceCopy, diagnostic.OpOpen, 1)
		}, want{"copy", "open", "open", "busy", 0}},
		{"open folder locked", func(e *trailEnv) {
			e.ready()
			e.lockFolder()
			e.mustPost("/copy/api/copy", `{"action":"start"}`, 200)
			e.reported(diagnostic.SourceCopy, diagnostic.OpOpen, 1)
		}, want{"copy", "open", "open", "busy", 0}},
		{"open unwritable folder", func(e *trailEnv) {
			e.ready()
			if err := os.WriteFile(e.folder, []byte("synthetic"), 0o644); err != nil {
				e.t.Fatal(err)
			}
			e.mustPost("/copy/api/copy", `{"action":"start"}`, 200)
			e.reported(diagnostic.SourceCopy, diagnostic.OpOpen, 1)
		}, want{"copy", "open", "open", "not_a_directory", 0}},
		{"pass folder locked later", func(e *trailEnv) {
			e.ready()
			e.pass(1)
			e.lockFolder()
			e.mustPost("/copy/api/copy", `{"action":"check"}`, 200)
			e.reported(diagnostic.SourceCopy, diagnostic.OpPass, 2)
		}, want{"copy", "pass", "open", "busy", 0}},
		{"other telescope", func(e *trailEnv) {
			a := e.account()
			if code, body := e.start(testSettings()); code != 200 {
				e.t.Fatalf("Connect to A = %d %s", code, body)
			}
			a.markReady("synthetic-device-a")
			e.settings()
			e.pass(1)
			a.stop()
			if code, body := e.start(testSettings()); code != 200 {
				e.t.Fatalf("Connect to B = %d %s", code, body)
			}
			a.markReady("synthetic-device-b")
			e.until("copying to end", func() bool {
				e.post("/copy/api/copy", `{"action":"check"}`)
				return !e.s.copy.Busy()
			})
			e.reported(diagnostic.SourceCopy, diagnostic.OpOtherTelescope, 1)
		}, want{"copy", "other_telescope", "other_telescope", "operation_failed", 0}},
		{"pass walk fails", func(e *trailEnv) {
			e.ready()
			e.fail(stepWalk, errSynthetic)
			e.pass(1)
		}, want{"copy", "pass", "pass", "operation_failed", 0}},
		{"pass with failed files", func(e *trailEnv) {
			e.ready()
			e.fail("open:"+trailFile, errSynthetic)
			e.pass(1)
		}, want{"copy", "pass", "pass", "none", 1}},
		{"preview not set up", func(e *trailEnv) {
			e.lan("192.0.2.10")
			e.mustPost("/copy/api/copy", `{"action":"preview"}`, 409)
		}, want{"copy", "preview", "preview", "operation_failed", 0}},
		{"preview walk fails", func(e *trailEnv) {
			e.ready()
			e.fail(stepWalk, errSynthetic)
			e.mustPost("/copy/api/copy", `{"action":"preview"}`, 200)
			e.reported(diagnostic.SourceCopy, diagnostic.OpPreview, 1)
		}, want{"copy", "preview", "preview", "operation_failed", 0}},
		{"test", func(e *trailEnv) {
			e.mustPost("/copy/api/test", `{"address":"192.0.2.99"}`, 200)
		}, want{"copy", "test", "test", "operation_failed", 0}},
		{"save settings", func(e *trailEnv) {
			e.fail(stepSave, errSynthetic)
			folder, _ := json.Marshal(e.folder)
			e.mustPost("/copy/api/settings", `{"folder":`+string(folder)+`,"interval":"5","files_at_once":"4","excludes":[],"includes":[],"skip_built_in":true,"start_copying":false}`, 500)
		}, want{"copy", "save_settings", "save_settings", "operation_failed", 0}},
		{"clear history locked", func(e *trailEnv) {
			e.settings()
			e.lockHistory()
			e.mustPost("/copy/api/clear", `{}`, 409)
		}, want{"copy", "clear_history", "open", "busy", 0}},
		{"choose folder", func(e *trailEnv) {
			e.fail(stepFolder, errSynthetic)
			e.mustPost("/copy/api/folder", `{"folder":""}`, 500)
		}, want{"copy", "choose_folder", "choose_folder", "operation_failed", 0}},
		{"browse connect", func(e *trailEnv) {
			e.lan("192.0.2.10")
			e.fail(stepDial, errSynthetic)
			e.browse()
			e.reported(diagnostic.SourceBrowse, diagnostic.OpConnect, 1)
		}, want{"browse", "connect", "connect", "operation_failed", 0}},
		{"browse records", func(e *trailEnv) {
			e.ready()
			e.pass(1)
			e.stopCopying()
			e.corruptHistory()
			e.browse()
			e.reported(diagnostic.SourceBrowse, diagnostic.OpRecords, 1)
		}, want{"browse", "records", "records", "operation_failed", 0}},
		{"browse read", func(e *trailEnv) {
			e.lan("192.0.2.10")
			e.fail(stepReadDir, errSynthetic)
			e.browse()
			e.reported(diagnostic.SourceBrowse, diagnostic.OpRead, 1)
		}, want{"browse", "read", "read", "operation_failed", 0}},
		{"browse save file", func(e *trailEnv) {
			e.lan("192.0.2.10")
			e.fail(stepSaveAs, errSynthetic)
			e.get("/browse/file?path=" + trailFile + "&download=1")
			e.reported(diagnostic.SourceBrowse, diagnostic.OpSaveFile, 1)
		}, want{"browse", "save_file", "save_file", "operation_failed", 0}},
		{"free up space preview not set up", func(e *trailEnv) {
			e.lan("192.0.2.10")
			e.mustPost("/copy/api/delete", `{"action":"find","days":"0","trust":false}`, 409)
		}, want{"delete", "preview", "preview", "operation_failed", 0}},
		{"free up space preview walk fails", func(e *trailEnv) {
			e.ready()
			e.pass(1)
			e.stopCopying()
			e.fail(stepWalk, errSynthetic)
			e.mustPost("/copy/api/delete", `{"action":"find","days":"0","trust":false}`, 200)
			e.reported(diagnostic.SourceDelete, diagnostic.OpPreview, 1)
		}, want{"delete", "preview", "preview", "operation_failed", 0}},
		{"free up space remover fails", func(e *trailEnv) {
			e.ready()
			e.pass(1)
			e.stopCopying()
			id := e.freePreview("0")
			e.fail(stepRemover, errSynthetic)
			e.confirm(id)
		}, want{"delete", "delete", "connect", "operation_failed", 0}},
		{"free up space with failed files", func(e *trailEnv) {
			e.ready()
			e.pass(1)
			e.stopCopying()
			id := e.freePreview("0")
			e.breakLocalCopy()
			e.confirm(id)
		}, want{"delete", "delete", "delete", "none", 1}},
		{"free up space options", func(e *trailEnv) {
			e.ready()
			e.fail(stepSave, errSynthetic)
			e.mustPost("/copy/api/delete", `{"action":"find","days":"3","trust":false}`, 409)
		}, want{"delete", "save_options", "save_options", "operation_failed", 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTrailEnv(t)
			tc.run(e)
			code := diagnostic.Code(tc.want.source, tc.want.op, true)
			e.until("the "+code+" line", func() bool { return slices.Contains(codes(e.fileLines()), code) })
			lines := e.fileLines()
			if len(lines) != 1 {
				t.Fatalf("file-tool lines = %v, want one %s", lines, code)
			}
			l := lines[0]
			got := want{fmt.Sprint(l["source"]), fmt.Sprint(l["op"]), fmt.Sprint(l["phase"]), fmt.Sprint(l["reason"]), 0}
			if n, ok := l["count"].(float64); ok {
				got.count = int(n)
			}
			if got != tc.want {
				t.Errorf("line = %+v, want %+v", got, tc.want)
			}
			msgs := fileMessages[opKey{tc.want.source, tc.want.op}]
			if m := l["message"]; m != msgs.failed && m != msgs.failedFiles {
				t.Errorf("message = %v", m)
			}
			if f := e.status().Failures; len(f) != 1 || f[0].Code != code || f[0].Phase != tc.want.phase || f[0].Reason != tc.want.reason {
				t.Errorf("failures = %+v", f)
			}
		})
	}
}

// corruptHistory overwrites every file of the history folder, so loading
// the copy folder's records fails.
func (e *trailEnv) corruptHistory() {
	e.t.Helper()
	n := 0
	err := filepath.WalkDir(e.stateDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		return os.WriteFile(p, []byte("synthetic garbage\n"), 0o600)
	})
	if err != nil || n == 0 {
		e.t.Fatalf("corrupt history: %d files, %v", n, err)
	}
}

// breakLocalCopy puts a file where the local copy's folder is, so
// proving the copy fails locally and a deletion fails that file and goes
// on. It returns the copy's folder to its place. Windows reads the
// missing folder as a missing copy, which is kept rather than failed, so
// the test is skipped there.
func (e *trailEnv) breakLocalCopy() (restore func()) {
	e.t.Helper()
	if runtime.GOOS == "windows" {
		e.t.Skip("Windows reads a file in place of a folder as a missing copy")
	}
	dir := filepath.Join(e.folder, "MyWorks")
	aside := filepath.Join(e.folder, "aside")
	if err := os.Rename(dir, aside); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("synthetic"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return func() {
		e.t.Helper()
		if err := os.Remove(dir); err != nil {
			e.t.Fatal(err)
		}
		if err := os.Rename(aside, dir); err != nil {
			e.t.Fatal(err)
		}
	}
}

// fileCodes is the codes of gui.log's file-tool lines, in order.
func (e *trailEnv) fileCodes() []string { return codes(e.fileLines()) }

// A listing that fails to read from the telescope writes a failed line,
// and the next listing answered writes one recovered line and clears it.
func TestBrowseReadRecovers(t *testing.T) {
	e := newTrailEnv(t)
	e.lan("192.0.2.10")
	e.fail(stepReadDir, errSynthetic)
	if code := e.browse(); code == 200 {
		t.Fatal("the failing listing answered")
	}
	e.reported(diagnostic.SourceBrowse, diagnostic.OpRead, 1)
	e.fail(stepReadDir, nil)
	if code := e.browse(); code != 200 {
		t.Fatalf("listing = %d", code)
	}
	e.reported(diagnostic.SourceBrowse, diagnostic.OpRead, 2)
	if got, want := e.fileCodes(), []string{"browse_read_failed", "browse_read_recovered"}; !slices.Equal(got, want) {
		t.Errorf("codes = %v, want %v", got, want)
	}
	if f := e.failing(); len(f) != 0 {
		t.Errorf("failing = %v", f)
	}
}

// A canceled operation neither fails nor clears its operation: a pass
// stopped, a preview discarded, a listing whose client went away and a
// stale confirmation leave every failing operation, its line and its
// failure count as they were.
func TestCanceledIsNeither(t *testing.T) {
	e := newTrailEnv(t)
	e.ready()
	e.pass(1)
	e.stopCopying()
	stale := e.freePreview("0")
	e.fail(stepRemover, errSynthetic)
	e.confirm(stale)
	e.fail(stepRemover, nil)
	e.fail(stepWalk, errSynthetic)
	e.pass(2)
	e.stopCopying()
	e.mustPost("/copy/api/copy", `{"action":"preview"}`, 200)
	e.reported(diagnostic.SourceCopy, diagnostic.OpPreview, 1)
	e.fail(stepReadDir, errSynthetic)
	e.browse()
	e.reported(diagnostic.SourceBrowse, diagnostic.OpRead, 1)
	e.fail(stepWalk, nil)
	e.fail(stepReadDir, nil)
	lines, before := e.fileCodes(), e.status().Failures
	if want := []string{"delete_delete_failed", "copy_pass_failed", "copy_preview_failed", "browse_read_failed"}; !slices.Equal(lines, want) {
		t.Fatalf("codes = %v, want %v", lines, want)
	}

	walking, release := e.hold(stepWalk)
	e.mustPost("/copy/api/copy", `{"action":"start"}`, 200)
	<-walking
	e.stopCopying()
	// A stopped pass is not reported at all (PassDone returns first).
	if got := len(e.outcomes(diagnostic.SourceCopy, diagnostic.OpPass)); got != 2 {
		t.Errorf("a stopped pass reported: %d pass outcomes", got)
	}
	e.mustPost("/copy/api/copy", `{"action":"preview"}`, 200)
	<-walking
	e.mustPost("/copy/api/copy", `{"action":"cancel"}`, 200)
	if o := e.reported(diagnostic.SourceCopy, diagnostic.OpPreview, 2); !o.Canceled {
		t.Errorf("discarded preview = %+v", o)
	}
	release()

	reading, release := e.hold(stepReadDir)
	ctx, cancel := context.WithCancel(context.Background())
	r := request(e.s, "GET", "/browse/api/list?path=MyWorks", "").WithContext(ctx)
	r.Header.Del("X-Seestar-Token")
	done := make(chan struct{})
	go func() {
		defer close(done)
		serve(e.s, r)
	}()
	<-reading
	cancel()
	release()
	<-done
	// A listing whose client went away is not reported at all.
	if got := len(e.outcomes(diagnostic.SourceBrowse, diagnostic.OpRead)); got != 1 {
		t.Errorf("a listing gone away reported: %d read outcomes", got)
	}

	n := len(e.outcomes(diagnostic.SourceDelete, diagnostic.OpDelete))
	if code, body := e.post("/copy/api/delete", `{"action":"delete","id":`+strconv.Itoa(stale)+`,"folders":["MyWorks"]}`); code != 409 || !strings.Contains(body, "Preview again") {
		t.Errorf("stale confirmation = %d %s", code, body)
	}
	if got := len(e.outcomes(diagnostic.SourceDelete, diagnostic.OpDelete)); got != n {
		t.Errorf("a stale confirmation reported %d outcomes", got-n)
	}

	if got := e.fileCodes(); !slices.Equal(got, lines) {
		t.Errorf("codes = %v, want %v", got, lines)
	}
	after := e.status().Failures
	if len(after) != len(before) {
		t.Fatalf("failures = %+v, want %+v", after, before)
	}
	for i := range after {
		if after[i].Code != before[i].Code || after[i].Failures != before[i].Failures {
			t.Errorf("failure %d = %+v, want %+v", i, after[i], before[i])
		}
	}
}

// A dialog's Cancel neither fails nor clears: after the folder picker and
// the save dialog each fail once, a Cancel of each leaves both failing.
func TestDialogCancelKeepsFailure(t *testing.T) {
	e := newTrailEnv(t)
	e.lan("192.0.2.10")
	e.fail(stepFolder, errSynthetic)
	e.fail(stepSaveAs, errSynthetic)
	e.mustPost("/copy/api/folder", `{"folder":""}`, 500)
	e.get("/browse/file?path=" + trailFile + "&download=1")
	e.reported(diagnostic.SourceBrowse, diagnostic.OpSaveFile, 1)
	e.fail(stepFolder, nil)
	e.fail(stepSaveAs, nil)
	e.setAnswer("")
	e.mustPost("/copy/api/folder", `{"folder":""}`, 200)
	e.get("/browse/file?path=" + trailFile + "&download=1")
	for _, o := range []copyapp.Outcome{
		e.reported(diagnostic.SourceCopy, diagnostic.OpChooseFolder, 2),
		e.reported(diagnostic.SourceBrowse, diagnostic.OpSaveFile, 2),
	} {
		if !o.Canceled {
			t.Errorf("Cancel = %+v", o)
		}
	}
	if got, want := e.fileCodes(), []string{"copy_choose_folder_failed", "browse_save_file_failed"}; !slices.Equal(got, want) {
		t.Errorf("codes = %v, want %v", got, want)
	}
	if got, want := e.failing(), []string{"copy/choose_folder", "browse/save_file"}; !slices.Equal(got, want) {
		t.Errorf("failing = %v, want %v", got, want)
	}
}

// A deletion that Stop ended neither fails nor clears: after one fails
// with a failed file, a second, stopped while it removes, leaves Free up
// space failing with its count.
func TestDeleteStopKeepsFailure(t *testing.T) {
	e := newTrailEnv(t)
	e.ready()
	e.pass(1)
	e.stopCopying()
	id := e.freePreview("0")
	restore := e.breakLocalCopy()
	if o := e.confirm(id); o.Count != 1 {
		t.Fatalf("failed deletion = %+v", o)
	}
	restore()
	id = e.freePreview("0")
	removing, release := e.hold(stepRemove)
	defer release()
	n := len(e.outcomes(diagnostic.SourceDelete, diagnostic.OpDelete))
	e.mustPost("/copy/api/delete", `{"action":"delete","id":`+strconv.Itoa(id)+`,"folders":["MyWorks"]}`, 200)
	<-removing
	e.mustPost("/copy/api/delete", `{"action":"stop"}`, 200)
	release()
	if o := e.reported(diagnostic.SourceDelete, diagnostic.OpDelete, n+1); !o.Canceled {
		t.Errorf("stopped deletion = %+v", o)
	}
	if got, want := e.fileCodes(), []string{"delete_delete_failed"}; !slices.Equal(got, want) {
		t.Errorf("codes = %v, want %v", got, want)
	}
	if f := e.status().Failures; len(f) != 1 || f[0].Code != "delete_delete_failed" || f[0].Count != 1 {
		t.Errorf("failures = %+v", f)
	}
}

// shareFiles adds n files to 192.0.2.10's MyWorks, f00.jpg on, each of
// whose opens is a step named open:MyWorks/fNN.jpg, and returns their
// steps.
func (e *trailEnv) shareFiles(n int) []string {
	e.t.Helper()
	if _, err := e.share("192.0.2.10:445"); err != nil {
		e.t.Fatal(err)
	}
	dir := filepath.Dir(e.shareFile("192.0.2.10:445"))
	var steps []string
	for i := range n {
		name := fmt.Sprintf("f%02d.jpg", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("synthetic"), 0o644); err != nil {
			e.t.Fatal(err)
		}
		old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
		if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
			e.t.Fatal(err)
		}
		steps = append(steps, "open:MyWorks/"+name)
	}
	return steps
}

// Repeats are kept per operation: Browse's connect failing on every
// attempt and Copy's pass failing on every pass, for 50 rounds between
// connection lines, a Browse read failure and recovery, and Copy
// successes of other operations, write one line each and count 50.
func TestRepeatsAreKeyed(t *testing.T) {
	e := newTrailEnv(t)
	e.ready()
	e.fail(stepDial, errSynthetic)
	for i := range 50 {
		if code := e.browse(); code == 200 {
			t.Fatal("a listing answered without a connection")
		}
		e.reported(diagnostic.SourceBrowse, diagnostic.OpConnect, i+1)
		e.s.controller.log.add("Connected to your Seestar")
		if o := e.pass(i + 1); !o.Failed() || o.Count != 0 {
			t.Fatalf("pass %d = %+v", i+1, o)
		}
		e.stopCopying()
		e.mustPost("/copy/api/test", `{"address":"192.0.2.10"}`, 200)
		e.reported(diagnostic.SourceCopy, diagnostic.OpTest, i+1)
		e.s.controller.log.add("Stopped")
	}
	if got, want := e.fileCodes(), []string{"browse_connect_failed", "copy_pass_failed"}; !slices.Equal(got, want) {
		t.Errorf("codes = %v, want %v", got, want)
	}
	f := e.status().Failures
	if len(f) != 2 {
		t.Fatalf("failures = %+v", f)
	}
	for _, f := range f {
		if f.Failures != 50 {
			t.Errorf("%s/%s failures = %d, want 50", f.Source, f.Op, f.Failures)
		}
	}
	// A Browse read failing and recovering, once connected, is its own
	// key: it leaves Copy's pass failing with its count.
	e.fail(stepDial, nil)
	e.fail(stepReadDir, errSynthetic)
	e.browse()
	// The connect's outcome is sent after its waiters wake, so it can
	// trail the read's: the two keys' lines may interleave either way.
	// Waiting for it here only makes it reach the trail before the end.
	e.reported(diagnostic.SourceBrowse, diagnostic.OpConnect, 51)
	e.fail(stepReadDir, nil)
	if code := e.browse(); code != 200 {
		t.Fatalf("listing = %d", code)
	}
	got := e.fileCodes()
	if len(got) != 5 {
		t.Errorf("codes = %v, want five", got)
	}
	for prefix, want := range map[string][]string{
		"browse_connect_": {"browse_connect_failed", "browse_connect_recovered"},
		"browse_read_":    {"browse_read_failed", "browse_read_recovered"},
		"copy_pass_":      {"copy_pass_failed"},
	} {
		var own []string
		for _, c := range got {
			if strings.HasPrefix(c, prefix) {
				own = append(own, c)
			}
		}
		if !slices.Equal(own, want) {
			t.Errorf("%s codes = %v, want %v, in %v", prefix, own, want, got)
		}
	}
	if f := e.status().Failures; len(f) != 1 || f[0].Op != diagnostic.OpPass || f[0].Failures != 50 {
		t.Errorf("failures = %+v", f)
	}
}

// A failure class is its phase and reason: a pass's count of failed files
// moves in memory without a line, a new reason writes one, and a clean
// pass writes the recovery.
func TestClassChangeAndCounts(t *testing.T) {
	e := newTrailEnv(t)
	e.ready()
	steps := e.shareFiles(13)
	for _, s := range steps {
		e.fail(s, errSynthetic)
	}
	n := 0
	passWith := func(count int) {
		t.Helper()
		n++
		o := e.pass(n)
		e.stopCopying()
		if o.Count != count {
			t.Fatalf("pass %d = %+v, want count %d", n, o, count)
		}
		f := e.status().Failures
		if len(f) != 1 || f[0].Count != count {
			t.Fatalf("after pass %d failures = %+v, want count %d", n, f, count)
		}
	}
	passWith(13)
	for _, s := range steps[:9] {
		e.fail(s, nil)
	}
	passWith(4)
	e.fail(steps[9], nil)
	passWith(3)
	e.fail(steps[10], nil)
	e.fail(steps[11], nil)
	passWith(1)
	e.fail(stepWalk, fmt.Errorf("synthetic: %w", context.DeadlineExceeded))
	passWith(0)
	if f := e.status().Failures; f[0].Reason != "timeout" || f[0].Failures != 1 {
		t.Errorf("timeout class = %+v", f)
	}
	e.fail(stepWalk, nil)
	e.fail(steps[12], nil)
	n++
	if o := e.pass(n); o.Failed() {
		t.Fatalf("clean pass = %+v", o)
	}
	lines := e.fileLines()
	if got, want := codes(lines), []string{"copy_pass_failed", "copy_pass_failed", "copy_pass_recovered"}; !slices.Equal(got, want) {
		t.Fatalf("codes = %v, want %v", got, want)
	}
	if lines[0]["reason"] != "none" || lines[0]["count"] != float64(13) || lines[1]["reason"] != "timeout" {
		t.Errorf("lines = %v", lines)
	}
	if f := e.failing(); len(f) != 0 {
		t.Errorf("failing = %v", f)
	}
}

// Failures are kept per operation, not per source: Browse's connect
// recovering leaves its save_file failing.
func TestFailuresPerOp(t *testing.T) {
	e := newTrailEnv(t)
	e.lan("192.0.2.10")
	e.fail(stepSaveAs, errSynthetic)
	e.get("/browse/file?path=" + trailFile + "&download=1")
	e.reported(diagnostic.SourceBrowse, diagnostic.OpSaveFile, 1)
	e.fail(stepDial, errSynthetic)
	e.browse()
	e.reported(diagnostic.SourceBrowse, diagnostic.OpConnect, 1)
	if got, want := e.failing(), []string{"browse/save_file", "browse/connect"}; !slices.Equal(got, want) {
		t.Fatalf("failing = %v, want %v", got, want)
	}
	e.fail(stepDial, nil)
	if code := e.browse(); code != 200 {
		t.Fatalf("listing = %d", code)
	}
	if got, want := e.failing(), []string{"browse/save_file"}; !slices.Equal(got, want) {
		t.Errorf("failing = %v, want %v", got, want)
	}
	if got, want := e.fileCodes(), []string{"browse_save_file_failed", "browse_connect_failed", "browse_connect_recovered"}; !slices.Equal(got, want) {
		t.Errorf("codes = %v, want %v", got, want)
	}
}

// holdOutcome holds the first outcome of source's op that match accepts
// before it reaches the trail, and returns a channel closed once one is
// held, the held outcome's place, and the release.
func (e *trailEnv) holdOutcome(source, op string, match func(copyapp.Outcome) bool) (held <-chan struct{}, got *copyapp.Outcome, release func()) {
	in, out := make(chan struct{}), make(chan struct{})
	var once, done sync.Once
	var o copyapp.Outcome
	e.fmu.Lock()
	e.holdReport = func(r copyapp.Outcome) {
		if r.Source != source || r.Op != op || !match(r) {
			return
		}
		first := false
		once.Do(func() { o, first = r, true; close(in) })
		if first {
			<-out
		}
	}
	e.fmu.Unlock()
	release = func() { done.Do(func() { close(out) }) }
	e.t.Cleanup(release)
	return in, &o, release
}

func failed(o copyapp.Outcome) bool { return o.Failed() }

// within fails the test if f doesn't return in time.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s blocked", what)
	}
}

// A Report that blocks holds up only its own operation: while a pass's,
// a Browse connect's, a settings save's or a Free up space find's report
// is held, status, Stop, a pass start, a Browse listing and a route change
// all return, and
// Quit returns once it is released.
func TestBlockedReportDoesNotBlock(t *testing.T) {
	for _, tc := range []struct {
		name, source, op string
		run              func(e *trailEnv)
	}{
		{"pass", diagnostic.SourceCopy, diagnostic.OpPass, func(e *trailEnv) {
			e.fail(stepWalk, errSynthetic)
			e.post("/copy/api/copy", `{"action":"start"}`)
		}},
		{"browse connect", diagnostic.SourceBrowse, diagnostic.OpConnect, func(e *trailEnv) {
			e.fail(stepDial, errSynthetic)
			e.browse()
		}},
		{"save settings", diagnostic.SourceCopy, diagnostic.OpSaveSettings, func(e *trailEnv) {
			e.fail(stepSave, errSynthetic)
			folder, _ := json.Marshal(e.folder + "-other")
			e.post("/copy/api/settings", `{"folder":`+string(folder)+`,"interval":"5","files_at_once":"4","excludes":[],"includes":[],"skip_built_in":true,"start_copying":false}`)
		}},
		{"free up space find", diagnostic.SourceDelete, diagnostic.OpSaveOptions, func(e *trailEnv) {
			e.fail(stepSave, errSynthetic)
			e.post("/copy/api/delete", `{"action":"find","days":"3","trust":false}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTrailEnv(t)
			e.ready()
			held, _, release := e.holdOutcome(tc.source, tc.op, failed)
			ran := make(chan struct{})
			go func() {
				defer close(ran)
				tc.run(e)
			}()
			select {
			case <-held:
			case <-time.After(10 * time.Second):
				t.Fatal("no outcome was held")
			}
			within(t, "/api/status", func() { e.get("/api/status") })
			within(t, "/copy/api/status", func() { e.get("/copy/api/status") })
			within(t, "Stop", func() { e.post("/copy/api/copy", `{"action":"stop"}`) })
			within(t, "a pass start", func() { e.post("/copy/api/copy", `{"action":"start"}`) })
			within(t, "a Browse listing", func() { e.browse() })
			within(t, "a route change", func() {
				e.post("/api/route", `{"settings":{"route":"lan","lan_address":"192.0.2.11"}}`)
			})
			release()
			within(t, "the held operation", func() { <-ran })
			within(t, "Quit", e.s.copy.Quit)
		})
	}
}

// A failure reported late, after a newer success on its operation, can't
// reopen it: pool A's failed connect is held before the trail takes it,
// a settings save replaces the pool, and pool B's connect succeeds first.
func TestOldConnectFailureCannotReopen(t *testing.T) {
	e := newTrailEnv(t)
	e.ready()
	held, old, release := e.holdOutcome(diagnostic.SourceBrowse, diagnostic.OpConnect, failed)
	e.fail(stepDial, errSynthetic)
	go e.browse()
	<-held
	e.fail(stepDial, nil)
	e.folder += "-other"
	e.settings()
	if code := e.browse(); code != 200 {
		t.Fatalf("pool B's listing = %d", code)
	}
	ok := e.reported(diagnostic.SourceBrowse, diagnostic.OpConnect, 1)
	if ok.Failed() || ok.Seq <= old.Seq {
		t.Fatalf("pool B's connect = %+v, after %+v", ok, *old)
	}
	release()
	e.reported(diagnostic.SourceBrowse, diagnostic.OpConnect, 2)
	for _, c := range e.fileCodes() {
		if strings.HasPrefix(c, "browse_connect_") {
			t.Errorf("a connect line: %v", e.fileCodes())
		}
	}
	if f := e.failing(); len(f) != 0 {
		t.Errorf("failing = %v", f)
	}
}

// A listing that cross-clears an operation advances that operation's own
// watermark: a records failure held before the trail takes it can't
// reopen it after a later listing answered.
func TestCrossClearAdvancesWatermark(t *testing.T) {
	e := newTrailEnv(t)
	e.ready()
	e.pass(1)
	e.stopCopying()
	saved := map[string][]byte{}
	filepath.WalkDir(e.stateDir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			saved[p], _ = os.ReadFile(p)
		}
		return nil
	})
	e.corruptHistory()
	held, old, release := e.holdOutcome(diagnostic.SourceBrowse, diagnostic.OpRecords, failed)
	go e.browse()
	<-held
	for p, data := range saved {
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if code := e.browse(); code != 200 {
		t.Fatalf("listing = %d", code)
	}
	ok := e.reported(diagnostic.SourceBrowse, diagnostic.OpRecords, 1)
	if ok.Failed() || ok.Seq <= old.Seq {
		t.Fatalf("the listing's records = %+v, after %+v", ok, *old)
	}
	release()
	e.reported(diagnostic.SourceBrowse, diagnostic.OpRecords, 2)
	if got := e.fileCodes(); slices.Contains(got, "browse_records_failed") {
		t.Errorf("codes = %v", got)
	}
	if f := e.failing(); len(f) != 0 {
		t.Errorf("failing = %v", f)
	}
	l := e.s.controller.log
	l.mu.Lock()
	seq := l.lastSeq[opKey{diagnostic.SourceBrowse, diagnostic.OpRecords}]
	l.mu.Unlock()
	if seq != ok.Seq {
		t.Errorf("records watermark = %d, want the listing's %d", seq, ok.Seq)
	}
}

// The refusal of a copy.json is a lifecycle line in gui.log and the
// status events, with no path, and with no Mount there are no file-tool
// lines.
func TestRefusalLineSurvivesTrail(t *testing.T) {
	savedMount := newMount
	t.Cleanup(func() { newMount = savedMount })
	newMount = func(ctx context.Context, o copyapp.MountOptions) *copyapp.Mount {
		t.Error("a Mount was made for a refused copy.json")
		return savedMount(ctx, o)
	}
	var events []map[string]any
	root := refusedStart(t, t.Context(), `{"version":2,"folder":"/synthetic/PATH-SENTINEL"}`, func(c *runtimeClient, quit func(), _ string) {
		defer quit()
		code, reply := c.do("GET", "/api/status", "")
		var status struct {
			Events []map[string]any `json:"events"`
		}
		if err := json.Unmarshal([]byte(reply), &status); code != 200 || err != nil {
			t.Fatalf("status = %d %s: %v", code, reply, err)
		}
		events = status.Events
	})
	found := func(lines []map[string]any) bool {
		for _, l := range lines {
			if l["message"] == copyRefusedEvent && l["code"] == "lifecycle" {
				return true
			}
		}
		return false
	}
	lines := guiLog(t, root)
	if !found(lines) {
		t.Errorf("gui.log = %v; want the refusal", lines)
	}
	if !found(events) {
		t.Errorf("events = %v; want the refusal", events)
	}
	data, _ := os.ReadFile(filepath.Join(root, "gui.log"))
	if strings.Contains(string(data), "PATH-SENTINEL") || strings.Contains(string(data), filepath.ToSlash(root)) {
		t.Errorf("gui.log holds a path: %s", data)
	}
	for _, l := range lines {
		if l["source"] != diagnostic.SourceConnection {
			t.Errorf("a file-tool line: %v", l)
		}
	}
}

// No file-tool line, event or failure carries an error's text: sentinels
// in the fakes' errors, a file's name and the folder's path appear in
// none of gui.log, /api/status events and failures. A catalog's serial
// does appear, in its line and in device_catalog, as documented.
func TestTrailPrivacySentinels(t *testing.T) {
	e := newTrailEnv(t)
	e.ready()
	sentinel := errors.New("PATH-SENTINEL FILE-SENTINEL HOST-SENTINEL")
	e.pass(1)
	e.stopCopying()
	id := e.freePreview("0")
	e.fail(stepRemover, sentinel)
	e.confirm(id)
	e.fail(stepRemover, nil)
	e.fail(stepReadDir, sentinel)
	e.browse()
	e.reported(diagnostic.SourceBrowse, diagnostic.OpRead, 1)
	e.fail(stepReadDir, nil)
	e.fail(stepWalk, sentinel)
	e.pass(2)
	e.stopCopying()
	e.fail(stepWalk, nil)
	for _, s := range []string{stepSave, stepFolder, stepSaveAs} {
		e.fail(s, sentinel)
	}
	e.mustPost("/copy/api/folder", `{"folder":""}`, 500)
	e.get("/browse/file?path=" + trailFile + "&download=1")
	e.reported(diagnostic.SourceBrowse, diagnostic.OpSaveFile, 1)
	folder, _ := json.Marshal(e.folder + "-other")
	e.post("/copy/api/settings", `{"folder":`+string(folder)+`,"interval":"5","files_at_once":"4","excludes":[],"includes":[],"skip_built_in":true,"start_copying":false}`)
	e.reported(diagnostic.SourceCopy, diagnostic.OpSaveSettings, 2)
	e.fail(stepDial, sentinel)
	e.lan("192.0.2.11")
	e.browse()
	e.s.controller.log.addCatalog(cloudhttp.Catalog{Pairs: []cloudhttp.CatalogPair{{Model: "S30", Serial: "SERIAL-SENTINEL"}}, Count: 1, Complete: true})

	if got := len(e.status().Failures); got < 7 {
		t.Fatalf("only %d failures: %v", got, e.failing())
	}
	data, err := os.ReadFile(filepath.Join(e.s.storage.root, "gui.log"))
	if err != nil {
		t.Fatal(err)
	}
	_, reply := e.get("/api/status")
	var status struct {
		Events   jsontext.Value `json:"events"`
		Failures jsontext.Value `json:"failures"`
	}
	if err := json.Unmarshal([]byte(reply), &status); err != nil {
		t.Fatal(err)
	}
	escaped, _ := json.Marshal(e.folder)
	for where, text := range map[string]string{"gui.log": string(data), "events": string(status.Events), "failures": string(status.Failures)} {
		for _, s := range []string{"PATH-SENTINEL", "FILE-SENTINEL", "HOST-SENTINEL", "at_192.0.2.10", filepath.ToSlash(e.folder), string(escaped[1 : len(escaped)-1])} {
			if strings.Contains(text, s) {
				t.Errorf("%s holds %q", where, s)
			}
		}
	}
	if n := strings.Count(string(data), "SERIAL-SENTINEL"); n < 2 {
		t.Errorf("gui.log has the serial %d times; want the catalog line and device_catalog", n)
	}
	if !strings.Contains(string(status.Events), "SERIAL-SENTINEL") {
		t.Error("events lack the catalog's serial")
	}
}
