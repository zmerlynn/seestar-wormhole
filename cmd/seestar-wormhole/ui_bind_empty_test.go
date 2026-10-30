package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
)

func TestReviewBlankBindReachesWildcardListener(t *testing.T) {
	called := false
	runner := newUIRunnerWithDiscovery(t.TempDir(), io.Discard,
		func(context.Context, proxy.Config, cloud.Credentials) (commandSession, error) {
			t.Fatal("unexpected session")
			return nil, errors.New("unexpected")
		},
		func(_, addr string) (net.Listener, error) {
			called = true
			host, _, err := net.SplitHostPort(addr)
			if err != nil || host != "" {
				t.Error("expected unspecified socket host")
			}
			return nil, errors.New("injected stop before socket creation")
		},
		func(string, int, landiscovery.Options, *slog.Logger) (commandDiscovery, error) {
			t.Fatal("unexpected discovery")
			return nil, errors.New("unexpected")
		},
		func(context.Context) error { return nil })
	_ = runner(t.Context(), ui.Settings{Version: 1, Bind: "", Idle: "0", Discovery: false}, cloud.Credentials{}, proxy.NewStatus())
	if called {
		t.Fatal("blank Other bind passed all option validation and reached wildcard socket creation")
	}
}
