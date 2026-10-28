package main

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
	"io"
	"log/slog"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestUIListenerErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		device, local uint16
		err           error
		code          string
	}{
		{80, 80, os.ErrPermission, "http_permission"}, {80, 80, syscall.EADDRINUSE, "http_conflict"},
		{4700, 4700, syscall.EADDRINUSE, "port_conflict"}, {4700, 4700, errors.New("private"), "bind"},
	} {
		var e *ui.ActionError
		if !errors.As(uiListenerError(tc.device, tc.local, tc.err), &e) || e.Code != tc.code {
			t.Fatalf("want %s: %v", tc.code, e)
		}
	}
}

func TestUIRunnerWiresDiscoveryAndArmedBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	status := proxy.NewStatus()
	detail, err := cloud.ParseLANDetail([]byte(`{"code":200,"data":{"deviceSn":"synthetic","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic"}}`))
	if err != nil {
		t.Fatal(err)
	}
	discoveries := 0
	runner := newUIRunnerWithDiscovery(t.TempDir(), io.Discard,
		func(_ context.Context, cfg proxy.Config, _ cloud.Credentials) (commandSession, error) {
			if !cfg.LANDiscovery || cfg.Status != status {
				t.Fatal("GUI discovery/status lost")
			}
			return &discoveryCommandFixture{idleCommandSession: &idleCommandSession{done: make(chan struct{})}, detail: detail}, nil
		}, func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") },
		func(bind string, port int, _ landiscovery.Options, _ *slog.Logger) (commandDiscovery, error) {
			discoveries++
			if bind != "0.0.0.0" || port != landiscovery.Port {
				t.Fatal("wrong discovery options")
			}
			return &fakeUIDiscovery{}, nil
		}, func(context.Context) error { return nil })
	done := make(chan error, 1)
	go func() {
		done <- runner(ctx, ui.Settings{Version: 1, Bind: "0.0.0.0", Discovery: true, Idle: "2m", Ports: []string{"80:80"}}, cloud.Credentials{}, status)
	}()
	select {
	case <-status.Updates():
	case <-time.After(5 * time.Second):
		t.Fatal("never armed")
	}
	if !status.SaveBoundary(true) || status.SaveBoundary(false) {
		t.Fatal("wrong save boundary")
	}
	cancel()
	<-done
	if discoveries != 1 {
		t.Fatal("discovery not initialized")
	}
}

type fakeUIDiscovery struct{}

func (*fakeUIDiscovery) Serve(ctx context.Context, _ *landiscovery.Machine) error {
	<-ctx.Done()
	return ctx.Err()
}
func (*fakeUIDiscovery) Close() error { return nil }
