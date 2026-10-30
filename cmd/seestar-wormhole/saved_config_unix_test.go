//go:build unix

package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

type noHeadlessNetwork struct{ t *testing.T }

func (n noHeadlessNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	n.t.Fatal("unexpected cloud request")
	return nil, cloud.ErrConfig
}

func TestSavedConfigIdentityRoot(t *testing.T) {
	root := savedConfigRoot(t)
	dir := filepath.Join(root, "InterProcessKV")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "device_uuid")
	const identity = "synthetic-existing-installation"
	if err := os.WriteFile(path, []byte(identity), 0600); err != nil {
		t.Fatal(err)
	}
	o, err := parse([]string{"proxy", "--config", "--state-dir", root}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := cloud.EmailCredentials(o.savedEmail, "synthetic-password")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err = runProxy(ctx, o, io.Discard, func(_ context.Context, cfg proxy.Config) (commandSession, error) {
		called = true
		if cfg.StateDir != root {
			t.Fatal("config root not forwarded as identity root")
		}
		// Client construction reads identity locally; account I/O is demand-driven.
		client, err := cloudhttp.NewWithCredentials(cloudhttp.Config{StateDir: cfg.StateDir, Transport: noHeadlessNetwork{t}}, creds)
		if err != nil {
			t.Fatal(err)
		}
		client.Close()
		cancel()
		return &idleCommandSession{done: make(chan struct{})}, nil
	}, func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }, listenCommandDiscovery, func(context.Context) error { return nil })
	if err != nil || !called {
		t.Fatalf("called=%v err=%v", called, err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != identity {
		t.Fatal("identity replaced")
	}
	if _, err := os.Lstat(filepath.Join(root, "ui.lock")); !os.IsNotExist(err) {
		t.Fatal("headless touched UI lock")
	}
}

func TestSavedConfigDefaultRoot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG_CONFIG_HOME default is Linux-specific")
	}
	root := savedConfigRoot(t)
	base := filepath.Dir(root)
	target := filepath.Join(base, "seestar-wormhole")
	if err := os.Rename(root, target); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", base)
	o, err := parse([]string{"proxy", "--config"}, io.Discard)
	if err != nil || o.stateDir != target || o.savedEmail != "saved@example.invalid" {
		t.Fatalf("default root=%s err=%v", o.stateDir, err)
	}
}
