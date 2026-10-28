package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
)

type trackedUIListener struct {
	net.Listener
	closed atomic.Bool
}

func (l *trackedUIListener) Close() error { l.closed.Store(true); return l.Listener.Close() }
func TestHTTPFailureClosesEarlierListeners(t *testing.T) {
	for _, cause := range []error{os.ErrPermission, syscall.EADDRINUSE} {
		var opened []*trackedUIListener
		runner := newUIRunnerWithDiscovery(t.TempDir(), io.Discard,
			func(context.Context, proxy.Config, cloud.Credentials) (commandSession, error) {
				t.Error("session opened on bind failure")
				return nil, errors.New("unexpected")
			},
			func(string, string) (net.Listener, error) {
				if len(opened) == 2 {
					return nil, &net.OpError{Op: "listen", Net: "tcp", Err: &os.SyscallError{Syscall: "bind", Err: cause}}
				}
				l, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					return nil, err
				}
				w := &trackedUIListener{Listener: l}
				opened = append(opened, w)
				return w, nil
			},
			func(string, int, landiscovery.Options, *slog.Logger) (commandDiscovery, error) {
				t.Error("discovery opened on bind failure")
				return nil, errors.New("unexpected")
			},
			func(context.Context) error { return nil })
		err := runner(t.Context(), ui.Settings{Version: 1, Bind: "0.0.0.0", Discovery: true, Idle: "0", Ports: []string{"80:80"}}, cloud.Credentials{}, proxy.NewStatus())
		var e *ui.ActionError
		want := "http_permission"
		if cause == syscall.EADDRINUSE {
			want = "http_conflict"
		}
		if !errors.As(err, &e) || e.Code != want {
			t.Fatalf("class: %v", err)
		}
		for _, l := range opened {
			if !l.closed.Load() {
				t.Error("earlier listener leaked")
			}
		}
	}
}
func TestRunnerRefusesIncompatibleBeforeIO(t *testing.T) {
	runner := newUIRunnerWithDiscovery(t.TempDir(), io.Discard,
		func(context.Context, proxy.Config, cloud.Credentials) (commandSession, error) {
			t.Error("session opened")
			return nil, errors.New("unexpected")
		},
		func(string, string) (net.Listener, error) {
			t.Error("listener opened")
			return nil, errors.New("unexpected")
		},
		func(string, int, landiscovery.Options, *slog.Logger) (commandDiscovery, error) {
			t.Error("discovery opened")
			return nil, errors.New("unexpected")
		},
		func(context.Context) error { t.Error("TCP check called"); return nil })
	if err := runner(t.Context(), ui.Settings{Version: 1, Bind: "127.0.0.1", Discovery: true, Idle: "0"}, cloud.Credentials{}, proxy.NewStatus()); err == nil {
		t.Fatal("incompatible accepted")
	}
}

func TestNoListenersCannotArmLazyRun(t *testing.T) {
	status := proxy.NewStatus()
	runner := newUIRunnerWithDiscovery(t.TempDir(), io.Discard,
		func(context.Context, proxy.Config, cloud.Credentials) (commandSession, error) {
			return &idleCommandSession{done: make(chan struct{})}, nil
		},
		func(string, string) (net.Listener, error) {
			t.Error("unexpected listener")
			return nil, errors.New("unexpected")
		},
		func(string, int, landiscovery.Options, *slog.Logger) (commandDiscovery, error) {
			t.Error("unexpected discovery")
			return nil, errors.New("unexpected")
		},
		func(context.Context) error { return nil })
	s := ui.Settings{Version: 1, Bind: "127.0.0.1", Idle: "2m", Ports: []string{"0:4700", "0:445", "0:80", "0:4350", "0:4554", "0:4555", "0:4801", "0:4800", "0:4804", "0:4361"}}
	if err := runner(t.Context(), s, cloud.Credentials{}, status); err == nil {
		t.Fatal("no-listener run unexpectedly succeeded")
	}
	if status.SaveBoundary(true) {
		t.Fatal("failed run with no listeners marked armed: lazy controller will overwrite saved settings")
	}
}
