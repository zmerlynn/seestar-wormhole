package main

import (
	"context"
	"io"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
)

// orderSession records its Close in the shared order.
type orderSession struct {
	*idleCommandSession
	record func(string)
}

func (s *orderSession) Close() error { s.record("close"); return s.idleCommandSession.Close() }

// The UI's run attaches its session for the file tools before it arms,
// and detaches before the session closes. A refused attach, Stop having
// come first, leaves the run unarmed.
func TestRunProxyAttachesBeforeArming(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		name := map[bool]string{false: "attached", true: "refused"}[refuse]
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var mu sync.Mutex
			var order []string
			record := func(what string) {
				mu.Lock()
				defer mu.Unlock()
				order = append(order, what)
			}
			status := proxy.NewStatus()
			var session *orderSession
			newSession := func(context.Context, proxy.Config, cloud.Credentials) (commandSession, error) {
				session = &orderSession{&idleCommandSession{done: make(chan struct{})}, record}
				return session, nil
			}
			attached := make(chan struct{})
			attach := func(d proxy.Dialer) (func(), bool) {
				defer close(attached)
				if status.Armed() {
					record("attach after arming")
				} else {
					record("attach")
				}
				if d != proxy.Dialer(session) {
					t.Errorf("attached %T, not the session", d)
				}
				if refuse {
					cancel() // Stop came first
					return func() { record("refused detach") }, false
				}
				return func() { record("detach") }, true
			}
			runner := newUIRunner(t.TempDir(), io.Discard, newSession, func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }, func(context.Context) error { return nil })
			done := make(chan error, 1)
			go func() {
				done <- runner(ctx, ui.Settings{Version: 1, Bind: "127.0.0.1", Idle: "0"}, cloud.Credentials{}, status, attach)
			}()
			select {
			case <-attached:
			case <-time.After(10 * time.Second):
				t.Fatal("the run never attached")
			}
			if !refuse {
				for deadline := time.Now().Add(10 * time.Second); !status.Armed(); time.Sleep(5 * time.Millisecond) {
					if time.Now().After(deadline) {
						t.Fatal("the run never armed")
					}
				}
				cancel()
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the run didn't end")
			}
			want := []string{"attach", "detach", "close"}
			if refuse {
				want = []string{"attach", "close"}
				if status.Armed() {
					t.Error("a refused attach armed the run")
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(order, want) {
				t.Errorf("order %q, want %q", order, want)
			}
		})
	}
}
