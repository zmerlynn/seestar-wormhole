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
	"sync/atomic"
	"syscall"
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
// serving the local ports serving, a JSON array, at the bind address.
func fakeWindow(t *testing.T, dir, bind, serving string) {
	t.Helper()
	fakeWindowFunc(t, dir, func() string { return `"serving_address":"` + bind + `","serving_ports":` + serving })
}

// fakeWindowFunc is fakeWindow with the status's serving fields as fields
// says each time it is asked.
func fakeWindowFunc(t *testing.T, dir string, fields func() string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" || r.Header.Get("X-Seestar-Token") != syntheticToken {
			http.Error(w, "Request rejected", http.StatusForbidden)
			return
		}
		io.WriteString(w, `{"future":true,`+fields()+`,"telescope_name":"Seestar S50 TEST0002"}`)
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
		name, bind, serving string
		profile             bool
		copyOnly            bool
	}{
		{"held by Garden", "0.0.0.0", "[" + strconv.Itoa(int(heldPort)) + "]", true, true},
		{"held by another program", "0.0.0.0", "[]", true, false},
		{"Garden serves another address", "127.0.0.1", "[" + strconv.Itoa(int(heldPort)) + "]", true, false},
		{"no profile", "0.0.0.0", "[" + strconv.Itoa(int(heldPort)) + "]", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			if _, err := ui.CreateProfile(root, "Garden"); err != nil {
				t.Fatal(err)
			}
			fakeWindow(t, ui.ProfileDir(root, "Garden"), tc.bind, tc.serving)
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

// A run's listeners are its window's from when they are bound: while the
// first run waits for its session, it already serves its ports, and a
// second profile's run whose port it holds is copy-only.
func TestUIRunServingBeforeSession(t *testing.T) {
	var ports []proxyrun.Port
	var served []string
	var free []net.Listener
	for _, device := range []uint16{4700, 445} {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		free = append(free, l)
		port := l.Addr().(*net.TCPAddr).Port
		ports = append(ports, proxyrun.Port{Local: uint16(port), Device: device})
		served = append(served, strconv.Itoa(port))
	}
	for _, l := range free {
		l.Close()
	}
	settings := ui.Settings{Version: 1, Bind: "127.0.0.1", Idle: "2m", Ports: ports}
	root := filepath.Join(t.TempDir(), "state")
	if _, err := ui.CreateProfile(root, "Garden"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	noDiscovery := func(string, int, landiscovery.Options, *slog.Logger) (commandDiscovery, error) {
		return &fakeUIDiscovery{}, nil
	}
	noTCPState := func(context.Context) error { return nil }

	// Default's window says what its run's status says.
	first := proxy.NewStatus()
	fakeWindowFunc(t, root, func() string {
		if bind, ok := first.Serving(); ok {
			return `"serving_address":"` + bind + `","serving_ports":[` + strings.Join(served, ",") + `]`
		}
		return `"serving_address":"","serving_ports":[]`
	})
	inSession := make(chan struct{})
	var firstLogs syncBuffer
	firstRun := newUIRunnerWithDiscovery(root, &firstLogs,
		func(ctx context.Context, _ proxy.Config, _ cloud.Credentials) (commandSession, error) {
			close(inSession)
			<-ctx.Done()
			return nil, ctx.Err()
		}, net.Listen, noDiscovery, noTCPState)
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- firstRun(ui.WithProfile(ctx, root, ui.DefaultProfile), settings, cloud.Credentials{}, first, func(proxy.Dialer) (func(), bool) { return func() {}, true })
	}()
	select {
	case <-inSession:
	case err := <-firstDone:
		t.Fatalf("the first run ended: %v\n%s", err, firstLogs.String())
	}
	if bind, ok := first.Serving(); !ok || bind != "127.0.0.1" || first.Armed() {
		t.Fatalf("waiting for its session: serving %q %t, armed %t", bind, ok, first.Armed())
	}

	second := proxy.NewStatus()
	var secondLogs syncBuffer
	secondRun := newUIRunnerWithDiscovery(ui.ProfileDir(root, "Garden"), &secondLogs,
		func(context.Context, proxy.Config, cloud.Credentials) (commandSession, error) {
			return &idleCommandSession{done: make(chan struct{})}, nil
		}, net.Listen, noDiscovery, noTCPState)
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- secondRun(ui.WithProfile(ctx, root, "Garden"), settings, cloud.Credentials{}, second, func(proxy.Dialer) (func(), bool) { return func() {}, true })
	}()
	select {
	case <-second.Updates():
	case err := <-secondDone:
		t.Fatalf("the second run ended: %v\n%s", err, secondLogs.String())
	case <-time.After(5 * time.Second):
		t.Fatal("the second run never armed")
	}
	if h, ok := second.CopyOnly(); !ok || h.Profile != "Default" {
		t.Errorf("second: holder %+v %t\n%s", h, ok, secondLogs.String())
	}
	cancel()
	<-secondDone
	<-firstDone
	if _, ok := first.Serving(); ok {
		t.Error("the first run still serves after it ended")
	}
}

// Another profile's window holds a port only at its own bind address:
// with one window's run on 127.0.0.2 and another program on 127.0.0.1 at
// one port, a run on 127.0.0.1 meets the port error, and is copy-only
// only when the window serves 127.0.0.1.
func TestUIRunCopyOnlyMatchesBindAddress(t *testing.T) {
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	port := uint16(other.Addr().(*net.TCPAddr).Port)
	window, err := net.Listen("tcp", net.JoinHostPort("127.0.0.2", strconv.Itoa(int(port))))
	if err != nil {
		t.Skipf("127.0.0.2 doesn't bind here: %v", err)
	}
	defer window.Close()
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	freePort := uint16(free.Addr().(*net.TCPAddr).Port)
	free.Close()
	settings := ui.Settings{Version: 1, Bind: "127.0.0.1", Idle: "2m",
		Ports: []proxyrun.Port{{Local: port, Device: 4700}, {Local: freePort, Device: 445}}}
	serving := "[" + strconv.Itoa(int(port)) + "," + strconv.Itoa(int(freePort)) + "]"

	for _, tc := range []struct {
		bind     string
		copyOnly bool
	}{{"127.0.0.2", false}, {"127.0.0.1", true}} {
		t.Run(tc.bind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			if _, err := ui.CreateProfile(root, "Garden"); err != nil {
				t.Fatal(err)
			}
			fakeWindow(t, ui.ProfileDir(root, "Garden"), tc.bind, serving)
			ctx, cancel := context.WithCancel(ui.WithProfile(t.Context(), root, ui.DefaultProfile))
			defer cancel()
			var logs syncBuffer
			status := proxy.NewStatus()
			runner := newUIRunnerWithDiscovery(root, &logs,
				func(context.Context, proxy.Config, cloud.Credentials) (commandSession, error) {
					return &idleCommandSession{done: make(chan struct{})}, nil
				}, net.Listen, func(string, int, landiscovery.Options, *slog.Logger) (commandDiscovery, error) {
					return &fakeUIDiscovery{}, nil
				}, func(context.Context) error { return nil })
			done := make(chan error, 1)
			go func() {
				done <- runner(ctx, settings, cloud.Credentials{}, status, func(proxy.Dialer) (func(), bool) { return func() {}, true })
			}()
			if !tc.copyOnly {
				if e, ok := errors.AsType[*ui.ActionError](<-done); !ok || e.Code != "port_conflict" || e.Local != port {
					t.Errorf("= %v, want the port conflict\n%s", e, logs.String())
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
			if h, ok := status.CopyOnly(); !ok || h.Profile != "Garden" {
				t.Errorf("holder %+v %t", h, ok)
			}
			cancel()
			<-done
		})
	}
}

// Two tabs' runs take their ports one after the other: a tab started
// while another run has bound only its first port waits for it to bind
// the rest and publish them, then finds the port held by that tab and
// runs copy-only rather than meeting the port error.
func TestUIRunPortsOneTabAtATime(t *testing.T) {
	var ports mappings
	for _, device := range []uint16{4700, 445} {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ports = append(ports, proxyrun.Port{Local: uint16(l.Addr().(*net.TCPAddr).Port), Device: device})
		l.Close()
	}
	var gate sync.Mutex
	run := func(status *proxy.Status, holder *proxy.Status, listen func(string, string) (net.Listener, error), session func(context.Context, proxy.Config) (commandSession, error)) error {
		o, err := parse([]string{"proxy"}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		o.command, o.bind, o.ports, o.uiStatus = commandUI, "127.0.0.1", ports, status
		o.uiPorts = func() func() { gate.Lock(); return gate.Unlock }
		// As the app's in-process lookup does, the other tab holds a
		// port only once its status says it serves.
		o.uiHolder = func(uint16) (proxy.Holder, bool) {
			_, ok := holder.Serving()
			return proxy.Holder{Profile: "Default", Tab: true}, ok
		}
		return runProxy(t.Context(), o, io.Discard, session, listen, nil, func(context.Context) error { return nil })
	}

	first, second := proxy.NewStatus(), proxy.NewStatus()
	atSecond, bind := make(chan struct{}), make(chan struct{})
	calls := 0
	ctx, cancel := context.WithCancel(t.Context())
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- run(first, second, func(network, address string) (net.Listener, error) {
			if calls++; calls == 2 {
				close(atSecond)
				<-bind
			}
			return net.Listen(network, address)
		}, func(context.Context, proxy.Config) (commandSession, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
	}()
	<-atSecond
	var listens atomic.Int32
	copyOnly := make(chan bool, 1)
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- run(second, first, func(network, address string) (net.Listener, error) {
			listens.Add(1)
			return net.Listen(network, address)
		}, func(context.Context, proxy.Config) (commandSession, error) {
			_, ok := second.CopyOnly()
			copyOnly <- ok
			return nil, errors.New("synthetic session boundary")
		})
	}()
	time.Sleep(50 * time.Millisecond)
	if n := listens.Load(); n != 0 {
		t.Errorf("the second tab bound %d ports while the first was binding", n)
	}
	close(bind)
	// Its session reports before it returns.
	err := <-secondDone
	select {
	case ok := <-copyOnly:
		if !ok {
			t.Error("the second tab isn't copy-only")
		}
	default:
		t.Fatalf("the second tab = %v before its session, not copy-only", err)
	}
	cancel()
	<-firstDone
}

// closeBlocked is a listener whose first Close waits for the test.
type closeBlocked struct {
	net.Listener
	closing chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (l *closeBlocked) Close() error {
	l.once.Do(func() { close(l.closing); <-l.release })
	return l.Listener.Close()
}

// A run that fails to bind its second port closes its first before
// another tab's run may bind: that run then meets no listener of a run
// that never served, and goes on to its session rather than a port error.
func TestUIRunPortsClosedBeforeTheNextTab(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closing, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() { unblock(); raw.Close() })
	held := &closeBlocked{Listener: raw, closing: closing, release: release}
	var gate sync.Mutex
	first := proxy.NewStatus()
	o, err := parse([]string{"proxy"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	o.command, o.bind, o.uiStatus = commandUI, "127.0.0.1", first
	o.ports = mappings{{Local: uint16(raw.Addr().(*net.TCPAddr).Port), Device: 4700}, {Local: 12346, Device: 445}}
	o.uiPorts = func() func() { gate.Lock(); return gate.Unlock }
	sessionErr := errors.New("synthetic session boundary")
	session := func(context.Context, proxy.Config) (commandSession, error) { return nil, sessionErr }
	calls := 0
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- runProxy(t.Context(), o, io.Discard, session, func(string, string) (net.Listener, error) {
			if calls++; calls == 1 {
				return held, nil
			}
			return nil, syscall.EACCES
		}, nil, func(context.Context) error { return nil })
	}()
	<-closing
	if gate.TryLock() {
		gate.Unlock()
		t.Error("the gate opened before the failed run closed its listener")
	}
	unblock()
	<-firstDone
	second := o
	second.uiStatus, second.ports = proxy.NewStatus(), o.ports[:1]
	second.uiHolder = func(uint16) (proxy.Holder, bool) {
		_, ok := first.Serving()
		return proxy.Holder{Profile: "Default", Tab: true}, ok
	}
	if err := runProxy(t.Context(), second, io.Discard, session, net.Listen, nil, func(context.Context) error { return nil }); !errors.Is(err, sessionErr) {
		t.Errorf("the next tab's run = %v, want its session", err)
	}
}
