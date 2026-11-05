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

	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// The two telescopes of a change of endpoint.
const (
	atA = "192.0.2.10:445"
	atB = "192.0.2.20:445"
)

// changeEnv is a Mount over two shares: freeEnv's at A, and at B one with
// a file of the same path, size and time but other contents.
type changeEnv struct {
	*freeEnv
	m     *Mount
	fileB string

	route    sync.Mutex
	endpoint string
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
		if endpoint == atB {
			return remote.NewFakeDir(dirB)
		}
		return e.share()
	}
	e.m = NewMount(context.Background(), MountOptions{
		Addr: testAddr, StateDir: e.stateDir, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Endpoint: func() (string, error) {
			e.route.Lock()
			defer e.route.Unlock()
			return e.endpoint, nil
		},
		Dial:    func(_ context.Context, endpoint string) (remote.Session, error) { return share(endpoint) },
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
// moved, not after a change of route, and not when the endpoint moves
// between the confirmation and the delete's dial.
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
}

// A change refuses while copying or Free up space runs, and runs nothing
// then; one that keeps the route goes ahead.
func TestChangeWaitsForWork(t *testing.T) {
	e := newChangeEnv(t)
	e.mu.Lock()
	e.hold = make(chan struct{})
	e.mu.Unlock()
	if w := e.post("/copy/api/delete", `{"action":"find","days":"0","trust":false}`); w.Code != 200 {
		t.Fatalf("find = %d %s", w.Code, w.Body)
	}
	committed := false
	if err := e.m.Change(func() bool { return true }, func() error { committed = true; return nil }); !errors.Is(err, ErrBusy) || committed {
		t.Errorf("change while checking = %v, committed %v", err, committed)
	}
	if err := e.m.Change(func() bool { return false }, func() error { committed = true; return nil }); err != nil || !committed {
		t.Errorf("change that keeps the route = %v, committed %v", err, committed)
	}
	close(e.hold)
	e.wait(frStart, frPreview, frNothing, frCheckFail)
	if err := e.m.Change(func() bool { return true }, func() error { return nil }); err != nil {
		t.Errorf("change once the check ended = %v", err)
	}
}
