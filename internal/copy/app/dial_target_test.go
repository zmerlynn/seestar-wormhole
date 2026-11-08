package app

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// TestDialsCarryTheTarget checks that every dial the Mount makes, for a
// copy pass, a copy preview, a Browse listing, a Free up space check and
// its delete, hands the host the whole Target, Run and Device included,
// so the host can choose the route from Run rather than from the label.
func TestDialsCarryTheTarget(t *testing.T) {
	e := newFreeEnv(t, "MyWorks/a.jpg")
	e.copy()
	want := Target{Endpoint: "Telescope (test label)", Device: "synthetic", Run: 7}
	var mu sync.Mutex
	got := map[string][]Target{}
	record := func(hook string, tg Target) {
		mu.Lock()
		defer mu.Unlock()
		got[hook] = append(got[hook], tg)
	}
	count := func(hook string) int {
		mu.Lock()
		defer mu.Unlock()
		return len(got[hook])
	}
	m := NewMount(context.Background(), MountOptions{
		Addr: testAddr, StateDir: e.stateDir, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Target: func() (Target, error) { return want, nil },
		Dial: func(_ context.Context, tg Target) (remote.Session, error) {
			record("dial", tg)
			return e.share()
		},
		Remover: func(_ context.Context, tg Target) (remote.Remover, error) {
			record("remover", tg)
			return e.share()
		},
	})
	t.Cleanup(m.Quit)
	e.f = m.s.free
	if err := m.s.file.save(e.set); err != nil {
		t.Fatal(err)
	}
	serve := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = testAddr.String()
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		m.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s %s = %d %s", method, path, w.Code, w.Body)
		}
		return w
	}
	waitFor := func(what string, done func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !done(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("waiting for %s", what)
			}
		}
	}

	serve("GET", "/browse/api/list", "")
	n := count("dial")
	if n == 0 {
		t.Fatal("the Browse listing dialed nothing")
	}
	serve("POST", "/copy/api/copy", `{"action":"preview"}`)
	waitFor("the preview's dial", func() bool { return count("dial") > n })
	n = count("dial")
	serve("POST", "/copy/api/delete", `{"action":"find","days":"0","trust":false}`)
	v := e.wait(frPreview, frNothing, frCheckFail)
	if v.State != frPreview {
		t.Fatalf("free preview: %+v", v)
	}
	if count("dial") <= n {
		t.Fatal("the Free up space check dialed nothing")
	}
	serve("POST", "/copy/api/delete", `{"action":"delete","id":`+strconv.Itoa(v.ID)+`,"folders":["MyWorks"]}`)
	if v := e.wait(frDone, frStopped, frFailed); v.State != frDone {
		t.Fatalf("delete: %+v", v)
	}
	n = count("dial")
	serve("POST", "/copy/api/copy", `{"action":"start"}`)
	waitFor("the pass's dial", func() bool { return count("dial") > n })

	mu.Lock()
	defer mu.Unlock()
	if len(got["remover"]) != 1 {
		t.Errorf("remover opened %d times, want 1", len(got["remover"]))
	}
	for hook, tgs := range got {
		for i, tg := range tgs {
			if tg != want {
				t.Errorf("%s %d got %+v, want %+v", hook, i, tg, want)
			}
		}
	}
}
