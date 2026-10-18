package main

import (
	"context"
	"io"
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

func TestLANHTTPStartupWarning(t *testing.T) {
	probe, err := landiscovery.Listen("0.0.0.0", 0)
	if err != nil {
		t.Skip("requires an active LAN broadcast interface")
	}
	probe.Close()
	for _, tc := range []struct {
		args []string
		want int
	}{
		{nil, 1}, {[]string{"--port", "80:80"}, 0}, {[]string{"--port", "0:80"}, 1},
	} {
		ctx, cancel := context.WithCancel(context.Background())
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
		args := append([]string{"--bind=0.0.0.0", "--lan-discovery", "--idle=2m"}, tc.args...)
		err := runWithSession(ctx, append([]string{"proxy"}, args...), io.Discard, logs, newSession)
		cancel()
		if err != nil {
			t.Fatal(err)
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
	}
}
