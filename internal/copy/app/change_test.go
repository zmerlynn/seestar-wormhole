package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// The two telescopes of a change of endpoint.
const (
	atA = "192.0.2.10:445"
	atB = "192.0.2.20:445"
)

// changeEnv is a Mount over two shares: freeEnv's at A, and at B one with
// a file of the same path, size and time but other contents. A reconnect
// keeps the endpoint but starts another run, which reaches B there, as a
// local port reused for another telescope would. Its dials wait for
// freeEnv's hold.
type changeEnv struct {
	*freeEnv
	m     *Mount
	fileB string

	route    sync.Mutex
	endpoint string
	run      uint64
}

func newChangeEnv(t *testing.T) *changeEnv {
	t.Helper()
	e := &changeEnv{freeEnv: newFreeEnv(t, "MyWorks/a.jpg"), endpoint: atA}
	e.copy()
	dirB := t.TempDir()
	e.fileB = filepath.Join(dirB, "MyWorks", "a.jpg")
	if err := os.MkdirAll(filepath.Dir(e.fileB), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.fileB, []byte("XYZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(e.fileB, ancient, ancient); err != nil {
		t.Fatal(err)
	}
	share := func(endpoint string) (*remote.FakeDir, error) {
		e.route.Lock()
		reused := e.run > 0
		e.route.Unlock()
		if endpoint == atB || reused {
			return remote.NewFakeDir(dirB)
		}
		return e.share()
	}
	e.m = NewMount(context.Background(), MountOptions{
		Addr: testAddr, StateDir: e.stateDir, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Target: func() (Target, error) {
			e.route.Lock()
			defer e.route.Unlock()
			return Target{Endpoint: e.endpoint, Run: e.run}, nil
		},
		Dial: func(ctx context.Context, endpoint string) (remote.Session, error) {
			e.mu.Lock()
			hold := e.hold
			e.mu.Unlock()
			if hold != nil {
				select {
				case <-hold:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return share(endpoint)
		},
		Remover: func(_ context.Context, endpoint string) (remote.Remover, error) { return share(endpoint) },
	})
	t.Cleanup(e.m.Quit)
	e.f = e.m.s.free
	if err := e.m.s.file.save(e.set); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *changeEnv) move(endpoint string) {
	e.route.Lock()
	defer e.route.Unlock()
	e.endpoint = endpoint
}

// reconnect starts another run at the same endpoint.
func (e *changeEnv) reconnect() {
	e.route.Lock()
	defer e.route.Unlock()
	e.run++
}

func (e *changeEnv) post(path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Host = testAddr.String()
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.m.ServeHTTP(w, r)
	return w
}

// preview runs Free up space's check at A to a preview.
func (e *changeEnv) preview() freeView {
	e.t.Helper()
	if w := e.post("/copy/api/delete", `{"action":"find","days":"0","trust":false}`); w.Code != 200 {
		e.t.Fatalf("find = %d %s", w.Code, w.Body)
	}
	v := e.wait(frPreview, frNothing, frCheckFail)
	if v.State != frPreview {
		e.t.Fatalf("preview: %+v", v)
	}
	return v
}

func (e *changeEnv) delete(v freeView) *httptest.ResponseRecorder {
	return e.post("/copy/api/delete", `{"action":"delete","id":`+strconv.Itoa(v.ID)+`,"folders":["MyWorks"]}`)
}

func (e *changeEnv) keptB() bool {
	b, err := os.ReadFile(e.fileB)
	return err == nil && string(b) == "XYZ"
}

// A preview checked at A never deletes at B: not once the endpoint has
// moved or the run has, not after a change of route, and not when the
// endpoint or the run moves between the confirmation and the delete's
// dial.
func TestDeleteStaysAtItsEndpoint(t *testing.T) {
	t.Run("moved", func(t *testing.T) {
		e := newChangeEnv(t)
		v := e.preview()
		e.move(atB)
		if w := e.delete(v); w.Code != 409 || !strings.Contains(w.Body.String(), "Preview again") {
			t.Errorf("delete after a move = %d %s", w.Code, w.Body)
		}
		if !e.keptB() || !e.has("MyWorks/a.jpg") {
			t.Error("a file was deleted")
		}
	})
	t.Run("reconnected", func(t *testing.T) {
		e := newChangeEnv(t)
		v := e.preview()
		e.reconnect()
		if w := e.delete(v); w.Code != 409 || !strings.Contains(w.Body.String(), "Preview again") {
			t.Errorf("delete after a reconnect = %d %s", w.Code, w.Body)
		}
		if !e.keptB() || !e.has("MyWorks/a.jpg") {
			t.Error("a file was deleted")
		}
	})
	t.Run("changed", func(t *testing.T) {
		e := newChangeEnv(t)
		v := e.preview()
		if err := e.m.Change(func() bool { return true }, func() error { e.move(atB); return nil }); err != nil {
			t.Fatal(err)
		}
		if got := e.f.view(); got.State != frStart {
			t.Errorf("preview after a change: %+v", got)
		}
		e.move(atA)
		if w := e.delete(v); w.Code != 409 {
			t.Errorf("delete after a change and back = %d %s", w.Code, w.Body)
		}
		if !e.has("MyWorks/a.jpg") {
			t.Error("A's file was deleted from a preview a change discarded")
		}
	})
	t.Run("moved at the dial", func(t *testing.T) {
		e := newChangeEnv(t)
		v := e.preview()
		e.f.prepared = func() { e.move(atB) }
		if w := e.delete(v); w.Code != 200 {
			t.Fatalf("delete = %d %s", w.Code, w.Body)
		}
		got := e.wait(frDone, frStopped, frFailed)
		if got.State != frFailed || !e.keptB() || !e.has("MyWorks/a.jpg") {
			t.Errorf("a delete followed the endpoint: %+v, B kept %v", got, e.keptB())
		}
	})
	t.Run("reconnected at the dial", func(t *testing.T) {
		e := newChangeEnv(t)
		v := e.preview()
		e.f.prepared = e.reconnect
		if w := e.delete(v); w.Code != 200 {
			t.Fatalf("delete = %d %s", w.Code, w.Body)
		}
		got := e.wait(frDone, frStopped, frFailed)
		if got.State != frFailed || !e.keptB() || !e.has("MyWorks/a.jpg") {
			t.Errorf("a delete followed the run: %+v, B kept %v", got, e.keptB())
		}
	})
}

// A change refuses while a Free up space check, a preview or a pass of
// copying runs, whether or not it moves the route, and commits nothing
// then. Between passes, copying refuses only a change that moves the
// route.
func TestChangeWaitsForWork(t *testing.T) {
	e := newChangeEnv(t)
	refused := func(what string) {
		t.Helper()
		for _, moves := range []bool{true, false} {
			committed := false
			if err := e.m.Change(func() bool { return moves }, func() error { committed = true; return nil }); !errors.Is(err, ErrBusy) || committed {
				t.Errorf("change (moves %v) while %s = %v, committed %v", moves, what, err, committed)
			}
		}
	}
	held := func(start func()) func() {
		hold := make(chan struct{})
		e.mu.Lock()
		e.hold = hold
		e.mu.Unlock()
		start()
		return func() {
			e.mu.Lock()
			e.hold = nil
			e.mu.Unlock()
			close(hold)
		}
	}
	until := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	release := held(func() {
		if w := e.post("/copy/api/delete", `{"action":"find","days":"0","trust":false}`); w.Code != 200 {
			t.Fatalf("find = %d %s", w.Code, w.Body)
		}
	})
	refused("checking")
	release()
	e.wait(frStart, frPreview, frNothing, frCheckFail)

	release = held(func() {
		if w := e.post("/copy/api/copy", `{"action":"preview"}`); w.Code != 200 {
			t.Fatalf("preview = %d %s", w.Code, w.Body)
		}
	})
	refused("previewing")
	release()
	until("the preview", func() bool { return !e.m.s.preview.running() })

	release = held(func() {
		if w := e.post("/copy/api/copy", `{"action":"start"}`); w.Code != 200 {
			t.Fatalf("start copying = %d %s", w.Code, w.Body)
		}
	})
	until("a pass", e.m.s.copy.passing)
	refused("a pass runs")
	release()
	until("the pass", func() bool { return e.m.s.copy.view(time.Now()).State == stUpToDate })
	if err := e.m.Change(func() bool { return true }, func() error { return nil }); !errors.Is(err, ErrBusy) {
		t.Errorf("change that moves the route between passes = %v", err)
	}
	if err := e.m.Change(func() bool { return false }, func() error { return nil }); err != nil {
		t.Errorf("change that keeps the route between passes = %v", err)
	}
	e.m.s.copy.stop()
	until("copying to stop", func() bool { return !e.m.Busy() })
	if err := e.m.Change(func() bool { return true }, func() error { return nil }); err != nil {
		t.Errorf("change once all work ended = %v", err)
	}
}

// A pass that comes due while a change of target is admitted and not yet
// committed waits for the commit, then follows the target it left: a
// reconnect whose reused port reaches another telescope ends copying
// before any dial, and one that reaches the same telescope resumes there.
// The first pass fails b.jpg, so the next retries it; B's b.jpg differs.
func TestPassDuringChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		device string // the telescope the reconnect reaches
		want   string // b.jpg's copy
	}{
		{"other telescope", "synthetic-b", ""},
		{"same telescope", "synthetic-a", "XYZ"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newChangeEnv(t)
			e.put("MyWorks/b.jpg", ancient)
			fileB := filepath.Join(filepath.Dir(e.fileB), "b.jpg")
			if err := os.WriteFile(fileB, []byte("XYZ"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(fileB, ancient, ancient); err != nil {
				t.Fatal(err)
			}
			e.mu.Lock()
			e.faults = map[string]remote.Fault{"MyWorks/b.jpg": {OpenErr: errors.New("synthetic read failure")}}
			e.mu.Unlock()
			c := e.m.s.copy
			c.target = func() (Target, error) {
				e.route.Lock()
				defer e.route.Unlock()
				device := "synthetic-a"
				if e.run > 0 {
					device = tc.device
				}
				return Target{Endpoint: e.endpoint, Device: device, Run: e.run}, nil
			}
			e.m.s.target = c.target
			// Once armed, the next pass signals as it waits for admission
			// and as it dials.
			var mu sync.Mutex
			armed := false
			reached, release := make(chan string, 2), make(chan struct{})
			c.admitting = func() {
				mu.Lock()
				defer mu.Unlock()
				if armed {
					reached <- "admission"
				}
			}
			dial := c.dial
			c.dial = func(ctx context.Context, endpoint string) (remote.Session, error) {
				mu.Lock()
				held := armed
				armed = false
				mu.Unlock()
				if held {
					// The dial reaches the port once the change has
					// committed.
					reached <- "dial"
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return dial(ctx, endpoint)
			}
			until := func(what string, cond func() bool) {
				t.Helper()
				deadline := time.Now().Add(10 * time.Second)
				for !cond() {
					if time.Now().After(deadline) {
						t.Fatalf("timed out waiting for %s", what)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			if err := e.m.s.startCopying(); err != nil {
				t.Fatal(err)
			}
			until("the first pass", func() bool { return c.view(time.Now()).State == stFailures })

			mu.Lock()
			armed = true
			mu.Unlock()
			admitted, proceed := make(chan struct{}), make(chan struct{})
			changed := make(chan error, 1)
			go func() {
				changed <- e.m.Change(func() bool { return false }, func() error {
					close(admitted)
					<-proceed
					e.reconnect() // the same local port now reaches the run's telescope
					return nil
				})
			}()
			<-admitted
			var got string
			until("the next pass", func() bool {
				c.check()
				select {
				case got = <-reached:
					return true
				default:
					return false
				}
			})
			if got != "admission" {
				t.Errorf("the next pass reached its %s during an admitted change", got)
			}
			close(proceed)
			if err := <-changed; err != nil {
				t.Fatal(err)
			}
			close(release)
			local := filepath.Join(e.set.Folder, "MyWorks", "b.jpg")
			if tc.want == "" {
				until("copying to end", func() bool { _, err := os.Stat(local); return !c.on() || err == nil })
				if v := c.view(time.Now()); v.State != stCantWrite || !strings.Contains(v.Title, "another telescope") {
					t.Errorf("copying ended in %q: %q", v.State, v.Title)
				}
				if b, err := os.ReadFile(local); err == nil {
					t.Errorf("a pass copied the other telescope's b.jpg: %q", b)
				}
				return
			}
			until("the pass", func() bool { return c.view(time.Now()).State == stUpToDate })
			if b, err := os.ReadFile(local); err != nil || string(b) != tc.want {
				t.Errorf("b.jpg = %q, %v", b, err)
			}
		})
	}
}
