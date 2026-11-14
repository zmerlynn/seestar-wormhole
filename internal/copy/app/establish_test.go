package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// blockedUntil is a dial that fails as macOS fails it while its Local
// Network prompt is open, until its nth try, and counts its tries.
func blockedUntil[T any](n int32, tries *atomic.Int32, dial func() (T, error)) (T, error) {
	if tries.Add(1) < n {
		var zero T
		return zero, connectFailed(syscall.EPERM)
	}
	return dial()
}

// Every tab's first dial to an address on this network waits out macOS's
// Local Network prompt, as Test and Save do: Copy's preview, Browse, and
// Free up space's preview and delete. Through the account each dials once.
func TestEveryTabWaitsForLocalNetwork(t *testing.T) {
	account := Target{Device: "synthetic", Run: 1}
	t.Run("copy preview", func(t *testing.T) {
		for _, lan := range []bool{true, false} {
			waited := setRetry(t, "darwin")
			e := newPreviewEnv(t, fstest.MapFS{"MyWorks/a.jpg": image("a")})
			var tries atomic.Int32
			e.p.dial = func(ctx context.Context, tg Target) (remote.Session, error) {
				return blockedUntil(3, &tries, func() (remote.Session, error) { return e.c.dial(ctx, tg) })
			}
			at := Target{Endpoint: e.set.Address}
			if !lan {
				at = account
				at.Endpoint = e.set.Address
			}
			v := e.runAt(at)
			if lan && (v.State != pvDone || tries.Load() != 3 || time.Duration(waited.Load()) != 2*time.Second) {
				t.Errorf("on this network: %d tries over %v, %+v", tries.Load(), time.Duration(waited.Load()), v)
			}
			if !lan && (v.State != pvFailed || tries.Load() != 1 || waited.Load() != 0) {
				t.Errorf("through the account: %d tries over %v, %+v", tries.Load(), time.Duration(waited.Load()), v)
			}
		}
	})
	t.Run("free up space", func(t *testing.T) {
		e := newFreeEnv(t, "MyWorks/a.jpg")
		e.copy()
		share := e.f.dial
		waited := setRetry(t, "darwin")
		var tries atomic.Int32
		e.f.dial = func(ctx context.Context, tg Target) (remote.Session, error) {
			return blockedUntil(3, &tries, func() (remote.Session, error) { return share(ctx, tg) })
		}
		v := e.preview()
		if v.State != frPreview || tries.Load() != 3 || time.Duration(waited.Load()) != 2*time.Second {
			t.Fatalf("preview: %d tries over %v, %+v", tries.Load(), time.Duration(waited.Load()), v)
		}
		// The delete's own session waits too, as the app's first dial.
		waited = setRetry(t, "darwin")
		remover := e.f.remover
		var opens atomic.Int32
		e.f.remover = func(ctx context.Context, tg Target) (remote.Remover, error) {
			return blockedUntil(3, &opens, func() (remote.Remover, error) { return remover(ctx, tg) })
		}
		if err := e.f.confirm(v.ID, []string{"MyWorks"}, Target{Endpoint: e.set.Address}); err != nil {
			t.Fatal(err)
		}
		if d := e.wait(frDone, frFailed, frStopped); d.State != frDone || opens.Load() != 3 || e.has("MyWorks/a.jpg") ||
			time.Duration(waited.Load()) != 2*time.Second {
			t.Errorf("delete: %d opens over %v, %+v", opens.Load(), time.Duration(waited.Load()), d)
		}
		// Through the account, a preview dials once.
		setRetry(t, "darwin")
		tries.Store(0)
		at := account
		at.Endpoint = e.set.Address
		if err := e.f.find(e.set, at); err != nil {
			t.Fatal(err)
		}
		if v := e.wait(frCheckFail, frPreview, frNothing); v.State != frCheckFail || tries.Load() != 1 {
			t.Errorf("through the account: %d tries, %+v", tries.Load(), v)
		}
	})
	t.Run("browse", func(t *testing.T) {
		for _, lan := range []bool{true, false} {
			waited := setRetry(t, "darwin")
			e := newAppEnv(t, savedAt(t.TempDir()))
			if !lan {
				e.device = "synthetic"
			}
			var tries atomic.Int32
			e.d.fail = func() error {
				_, err := blockedUntil(3, &tries, func() (struct{}, error) { return struct{}{}, nil })
				return err
			}
			w := e.serve("GET", "/browse/api/list", "")
			if lan && (w.Code != 200 || tries.Load() != 3 || time.Duration(waited.Load()) != 2*time.Second) {
				t.Errorf("on this network: %d tries over %v, %d %s", tries.Load(), time.Duration(waited.Load()), w.Code, w.Body)
			}
			if !lan && (w.Code != 502 || tries.Load() != 1 || waited.Load() != 0) {
				t.Errorf("through the account: %d tries over %v, %d %s", tries.Load(), time.Duration(waited.Load()), w.Code, w.Body)
			}
		}
	})
}

// A delete is never tried again: only opening its session is. A removal
// that fails as a blocked connect does ends the run.
func TestDeleteNotRetried(t *testing.T) {
	e := newFreeEnv(t, "MyWorks/a.jpg", "MyWorks/b.jpg")
	e.copy()
	v := e.preview()
	setRetry(t, "darwin")
	remover := e.f.remover
	var removes atomic.Int32
	e.f.remover = func(ctx context.Context, tg Target) (remote.Remover, error) {
		r, err := remover(ctx, tg)
		return blocked{r, &removes}, err
	}
	if err := e.f.confirm(v.ID, []string{"MyWorks"}, Target{Endpoint: e.set.Address}); err != nil {
		t.Fatal(err)
	}
	if d := e.wait(frDone, frFailed, frStopped); d.State != frFailed || removes.Load() != 1 {
		t.Errorf("%d removals, %+v", removes.Load(), d)
	}
}

// blocked is a share whose removals fail as a blocked connect, counted.
type blocked struct {
	remote.Remover
	n *atomic.Int32
}

func (b blocked) RemoveFile(ctx context.Context, en remote.Entry) error {
	b.n.Add(1)
	return connectFailed(syscall.EPERM)
}

// On macOS, Free up space and Browse say where to allow Local Network
// access when they can't reach an address on this network, as Copy and
// Test do; through the account they don't.
func TestEveryTabLocalNetworkHint(t *testing.T) {
	setHostOS(t, "darwin")
	lan, account := Target{Endpoint: testEndpoint}, Target{Endpoint: testEndpoint, Device: "synthetic", Run: 1}
	failed := connectFailed(syscall.EHOSTUNREACH)
	for _, tt := range []struct {
		st   freeStatus
		want string
	}{
		{freeStatus{state: frCheckFail, err: failed, target: lan}, "Preview again to retry." + lanHint},
		{freeStatus{state: frCheckFail, err: failed, target: account}, "Preview again to retry."},
		{freeStatus{state: frFailed, err: failed, target: lan}, "No files were deleted." + lanHint},
		{freeStatus{state: frFailed, err: failed, target: account}, "No files were deleted."},
		// An answer from the telescope is no Local Network failure.
		{freeStatus{state: frCheckFail, err: errors.New("access denied"), target: lan}, "Preview again to retry."},
	} {
		if v := tt.st.view(nil); v.Text != tt.want {
			t.Errorf("%s at %+v: %q, want %q", tt.st.state, tt.st.target, v.Text, tt.want)
		}
	}
	for _, device := range []string{"", "synthetic"} {
		setRetry(t, "darwin")
		e := newAppEnv(t, savedAt(t.TempDir()))
		e.device = device
		e.d.fail = func() error { return failed }
		w := e.serve("GET", "/browse/api/list", "")
		got := decode[struct {
			Message string `json:"message"`
		}](t, w)
		want := failed.Error()
		if device == "" {
			want += "." + lanHint
		}
		if w.Code != 502 || got.Message != want {
			t.Errorf("Browse with device %q: %d %q\nwant %q", device, w.Code, got.Message, want)
		}
	}
	// The hint keeps the error for classification.
	if err := withHint(failed); !errors.Is(err, syscall.EHOSTUNREACH) || !strings.HasSuffix(err.Error(), lanHint) {
		t.Errorf("withHint = %v", err)
	}
}
