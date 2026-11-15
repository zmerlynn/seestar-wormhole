package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
)

// syncBuffer is a log the run and the test share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

const syntheticToken = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

// fakeWindow answers as the open window of the profile folder dir,
// serving the local ports serving, a JSON array.
func fakeWindow(t *testing.T, dir string, serving string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" || r.Header.Get("X-Seestar-Token") != syntheticToken {
			http.Error(w, "Request rejected", http.StatusForbidden)
			return
		}
		io.WriteString(w, `{"future":true,"serving_ports":`+serving+`,"telescope_name":"Seestar S50 TEST0002"}`)
	}))
	t.Cleanup(server.Close)
	port := server.Listener.Addr().(*net.TCPAddr).Port
	f, err := privatefile.Open(filepath.Join(dir, "ui-runtime.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.WriteString(f, `{"port":`+strconv.Itoa(port)+`,"token":"`+syntheticToken+`"}`)
	if err = errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
}

// A UI run whose port the open window of another profile holds runs
// copy-only: it serves none of its ports and no discovery, names the
// holder, logs the port with the real error once, and still attaches its
// session for the file tools. A port held by anything else, or a run of
// no profile, keeps the port error.
func TestUIRunCopyOnly(t *testing.T) {
	held, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	heldPort := uint16(held.Addr().(*net.TCPAddr).Port)
	_, inUse := net.Listen("tcp", held.Addr().String())
	if inUse == nil {
		t.Fatal("listened twice on one port")
	}
	free, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	freePort := uint16(free.Addr().(*net.TCPAddr).Port)
	free.Close()
	settings := ui.Settings{Version: 1, Bind: "0.0.0.0", Discovery: true, Idle: "2m",
		Ports: []proxyrun.Port{{Local: freePort, Device: 4700}, {Local: heldPort, Device: 445}}}

	for _, tc := range []struct {
		name, serving string
		profile       bool
		copyOnly      bool
	}{
		{"held by Garden", "[" + strconv.Itoa(int(heldPort)) + "]", true, true},
		{"held by another program", "[]", true, false},
		{"no profile", "[" + strconv.Itoa(int(heldPort)) + "]", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			if _, err := ui.CreateProfile(root, "Garden"); err != nil {
				t.Fatal(err)
			}
			fakeWindow(t, ui.ProfileDir(root, "Garden"), tc.serving)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.profile {
				ctx = ui.WithProfile(ctx, root, ui.DefaultProfile)
			}
			var logs syncBuffer
			status := proxy.NewStatus()
			attached := false
			runner := newUIRunnerWithDiscovery(root, &logs,
				func(context.Context, proxy.Config, cloud.Credentials) (commandSession, error) {
					return &idleCommandSession{done: make(chan struct{})}, nil
				}, net.Listen,
				func(string, int, landiscovery.Options, *slog.Logger) (commandDiscovery, error) {
					t.Error("a copy-only run listened for discovery")
					return &fakeUIDiscovery{}, nil
				}, func(context.Context) error { return nil })
			done := make(chan error, 1)
			go func() {
				done <- runner(ctx, settings, cloud.Credentials{}, status, func(proxy.Dialer) (func(), bool) { attached = true; return func() {}, true })
			}()
			if !tc.copyOnly {
				err := <-done
				if e, ok := errors.AsType[*ui.ActionError](err); !ok || e.Code != "port_conflict" || e.Local != heldPort {
					t.Errorf("= %v, want the port conflict", err)
				}
				if _, ok := status.CopyOnly(); ok || strings.Contains(logs.String(), "copy_only") {
					t.Errorf("went copy-only:\n%s", logs.String())
				}
				return
			}
			select {
			case <-status.Updates():
			case err := <-done:
				t.Fatalf("ended: %v\n%s", err, logs.String())
			case <-time.After(5 * time.Second):
				t.Fatal("never armed")
			}
			if h, ok := status.CopyOnly(); !ok || h != (proxy.Holder{Profile: "Garden", Telescope: "Seestar S50 TEST0002"}) || !status.Armed() || !attached {
				t.Errorf("holder %+v %t, armed %t, attached %t", h, ok, status.Armed(), attached)
			}
			if c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(freePort)))); err == nil {
				c.Close()
				t.Error("the port before the held one is still served")
			}
			got := logs.String()
			if strings.Count(got, `"msg":"copy_only"`) != 1 || !strings.Contains(got, `"local_port":`+strconv.Itoa(int(heldPort))) ||
				!strings.Contains(got, inUse.Error()) || !strings.Contains(got, `"holder_profile":"Garden"`) {
				t.Errorf("logs:\n%s", got)
			}
			cancel()
			if err := <-done; err != nil {
				t.Errorf("= %v", err)
			}
		})
	}
}
