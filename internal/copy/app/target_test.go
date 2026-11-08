package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// targetEnv is a Mount over freeEnv's share at A whose target, once
// armed, reads A for the start's snapshot and later for every check
// after it. Its dials are recorded.
type targetEnv struct {
	*freeEnv
	m *Mount

	route  sync.Mutex
	armed  int // the target calls left that read A, or -1
	later  Target
	err    error
	dialed []string
}

func newTargetEnv(t *testing.T) *targetEnv {
	t.Helper()
	e := &targetEnv{freeEnv: newFreeEnv(t, "MyWorks/a.jpg"), armed: -1}
	e.copy()
	e.m = NewMount(context.Background(), MountOptions{
		Addr: testAddr, StateDir: e.stateDir, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Target: func() (Target, error) {
			e.route.Lock()
			defer e.route.Unlock()
			switch {
			case e.armed < 0:
				return Target{Endpoint: atA}, nil
			case e.armed > 0:
				e.armed--
				return Target{Endpoint: atA}, nil
			}
			return e.later, e.err
		},
		Dial: func(_ context.Context, tg Target) (remote.Session, error) {
			endpoint := tg.Endpoint
			e.route.Lock()
			e.dialed = append(e.dialed, endpoint)
			e.route.Unlock()
			return e.share()
		},
	})
	t.Cleanup(e.m.Quit)
	if err := e.m.s.file.save(e.set); err != nil {
		t.Fatal(err)
	}
	return e
}

// arm makes the target read A for the start and then later, or err.
func (e *targetEnv) arm(later Target, err error) {
	e.route.Lock()
	defer e.route.Unlock()
	e.armed, e.later, e.err, e.dialed = 1, later, err, nil
}

func (e *targetEnv) dials() []string {
	e.route.Lock()
	defer e.route.Unlock()
	return append([]string(nil), e.dialed...)
}

func (e *targetEnv) post(path, body string) {
	e.t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Host = testAddr.String()
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.m.ServeHTTP(w, r)
	if w.Code != 200 {
		e.t.Fatalf("%s %s = %d %s", path, body, w.Code, w.Body)
	}
}

// Copy's preview and Free up space's check dial only while the telescope
// is still at the target they started at: one that moved, reconnected or
// disconnected before the dial fails without dialing, and an unchanged
// one dials the address it started at.
func TestReadOnlyChecksStayAtTheirTarget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		later Target
		err   error
		ok    bool
	}{
		{name: "moved", later: Target{Endpoint: atB}},
		{name: "reconnected", later: Target{Endpoint: atA, Run: 1}},
		{name: "disconnected", err: errors.New("synthetic: not connected")},
		{name: "unchanged", later: Target{Endpoint: atA}, ok: true},
	} {
		t.Run("preview "+tc.name, func(t *testing.T) {
			e := newTargetEnv(t)
			e.arm(tc.later, tc.err)
			e.post("/copy/api/copy", `{"action":"preview"}`)
			deadline := time.Now().Add(10 * time.Second)
			v := e.m.s.preview.view()
			for v == nil || v.State == pvRunning {
				if time.Now().After(deadline) {
					t.Fatalf("preview: %+v", v)
				}
				time.Sleep(5 * time.Millisecond)
				v = e.m.s.preview.view()
			}
			want, dials := pvFailed, []string(nil)
			if tc.ok {
				want, dials = pvDone, []string{atA}
			}
			if v.State != want || !slices.Equal(e.dials(), dials) {
				t.Errorf("preview %+v after dials %q, want %s after %q", v, e.dials(), want, dials)
			}
		})
		t.Run("find "+tc.name, func(t *testing.T) {
			e := newTargetEnv(t)
			e.f = e.m.s.free
			e.arm(tc.later, tc.err)
			e.post("/copy/api/delete", `{"action":"find","days":"0","trust":false}`)
			v := e.wait(frPreview, frNothing, frCheckFail)
			want, dials := frCheckFail, []string(nil)
			if tc.ok {
				want, dials = frPreview, []string{atA}
			}
			if v.State != want || !slices.Equal(e.dials(), dials) {
				t.Errorf("find %+v after dials %q, want %s after %q", v, e.dials(), want, dials)
			}
		})
	}
}
