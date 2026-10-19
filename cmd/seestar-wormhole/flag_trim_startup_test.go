package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

type discoveryCommandFixture struct {
	*idleCommandSession
	detail cloud.Detail
}

func (s *discoveryCommandFixture) DeviceDetail() cloud.Detail { return s.detail }

func TestNewOptionStartupWiring(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &cancelOnLogWrite{cancel: cancel}
	called := false
	newSession := func(_ context.Context, cfg proxy.Config) (commandSession, error) {
		called = true
		if cfg.Serial != "PRIVATE_SERIAL_SENTINEL" || cfg.StateDir != "PRIVATE_PATH_SENTINEL" {
			t.Fatal("serial/storage flags not forwarded")
		}
		return idleCommandFixture(ctx, cfg)
	}
	err := runWithSession(ctx, append([]string{"proxy"}, []string{"--serial", "PRIVATE_SERIAL_SENTINEL", "--state-dir", "PRIVATE_PATH_SENTINEL", "--idle=2m"}...), io.Discard, logs, newSession)
	if err != nil || !called {
		t.Fatalf("startup err=%v called=%v", err, called)
	}
	if strings.Contains(logs.b.String(), "PRIVATE_") {
		t.Fatal("option value leaked")
	}
	if strings.Contains(logs.b.String(), "lan_http_port") {
		t.Fatal("HTTP warning without discovery")
	}
}

// Startup owns and joins discovery; no datagram I/O is needed to check warnings.
type idleCommandDiscovery struct{}

func (idleCommandDiscovery) Serve(ctx context.Context, _ *landiscovery.Machine) error {
	<-ctx.Done()
	return nil
}
func (idleCommandDiscovery) Close() error { return nil }

func TestLANHTTPStartupWarning(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		want      int
		http      string
		listeners int
	}{
		{"defaults", nil, 1, "8080", 10},
		{"http-80", []string{"--port", "80:80"}, 0, "80", 10},
		{"http-disabled", []string{"--port", "0:80"}, 1, "", 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			logs := &cancelOnLogWrite{cancel: cancel}
			newSession := func(_ context.Context, cfg proxy.Config) (commandSession, error) {
				if !cfg.LANDiscovery {
					t.Fatal("discovery flag not forwarded")
				}
				d, err := cloud.ParseLANDetail([]byte(`{"code":200,"data":{"deviceSn":"SYNTHETIC_SERIAL","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"Synthetic Device"}}`))
				if err != nil {
					t.Fatal(err)
				}
				return &discoveryCommandFixture{&idleCommandSession{done: make(chan struct{})}, d}, nil
			}
			args := append([]string{"proxy", "--bind=0.0.0.0", "--lan-discovery", "--idle=2m"}, tc.args...)
			o, err := parse(args, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			requested := make(map[string]bool)
			listen := func(network, address string) (net.Listener, error) {
				if network != "tcp" {
					t.Fatalf("network=%q want tcp", network)
				}
				requested[address] = true
				// Keep the configured mappings, especially 80:80, intact: the
				// warning uses those, not the listener's OS-assigned address.
				return net.Listen("tcp", "127.0.0.1:0")
			}
			discoveryCalls := 0
			listenDiscovery := func(bind string, port int, _ *slog.Logger) (commandDiscovery, error) {
				discoveryCalls++
				if bind != "0.0.0.0" || port != landiscovery.Port {
					t.Fatalf("discovery bind=%q port=%d", bind, port)
				}
				return idleCommandDiscovery{}, nil
			}
			err = runProxy(ctx, o, logs, newSession, listen, listenDiscovery, proxy.CheckTCPStateSupport)
			if err != nil {
				t.Fatal(err)
			}
			if len(requested) != tc.listeners || discoveryCalls != 1 {
				t.Fatalf("TCP listeners=%d want %d; discovery calls=%d want 1", len(requested), tc.listeners, discoveryCalls)
			}
			if tc.http != "" && !requested[net.JoinHostPort("0.0.0.0", tc.http)] {
				t.Fatalf("HTTP listener %s not requested: %v", tc.http, requested)
			}
			if tc.http == "" {
				for _, p := range o.ports {
					if p.device == 80 {
						t.Fatalf("disabled HTTP mapping retained: %d:80", p.local)
					}
				}
			}
			if got := strings.Count(logs.b.String(), `"msg":"lan_http_port"`); got != tc.want {
				t.Fatalf("HTTP warning count %d want %d", got, tc.want)
			}
			if tc.want == 1 && !strings.Contains(logs.b.String(), "album/image views need -p 80:80") {
				t.Fatal("missing phone requirement")
			}
			if !strings.Contains(logs.b.String(), "proxy_started") {
				t.Fatal("HTTP warning stopped startup")
			}
		})
	}
}
