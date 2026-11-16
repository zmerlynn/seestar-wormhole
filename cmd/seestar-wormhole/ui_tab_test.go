package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
)

// tabFolder is the profile a test's tab folder lookup gives a run.
type tabFolder struct{}

// withTabs has the UI runner find each run's folder from its context's
// tabFolder, as the app finds a tab's, and share one port gate, until the
// test ends.
func withTabs(t *testing.T, root string, gate *sync.Mutex) {
	runDir, portGate := uiRunDir, uiPortGate
	t.Cleanup(func() { uiRunDir, uiPortGate = runDir, portGate })
	uiRunDir = func(ctx context.Context, dir string) string {
		if name, ok := ctx.Value(tabFolder{}).(string); ok {
			return ui.ProfileDir(root, name)
		}
		return dir
	}
	uiPortGate = func(context.Context) func() { gate.Lock(); return gate.Unlock }
}

// freePorts is local ports for the devices that nothing listens on.
func freePorts(t *testing.T, devices ...uint16) []proxyrun.Port {
	t.Helper()
	var ports []proxyrun.Port
	var free []net.Listener
	for _, device := range devices {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		free = append(free, l)
		ports = append(ports, proxyrun.Port{Local: uint16(l.Addr().(*net.TCPAddr).Port), Device: device})
	}
	for _, l := range free {
		l.Close()
	}
	return ports
}

func noUIDiscovery(string, int, landiscovery.Options, *slog.Logger) (commandDiscovery, error) {
	return &fakeUIDiscovery{}, nil
}

// The UI runner gives each tab's session its own folder: Garden's run
// keeps Garden's installation identity rather than Default's.
func TestUIRunnerTabFolder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if _, err := ui.CreateProfile(root, "Garden"); err != nil {
		t.Fatal(err)
	}
	withTabs(t, root, new(sync.Mutex))
	settings := ui.Settings{Version: 1, Bind: "127.0.0.1", Idle: "2m", Ports: freePorts(t, 4700)}
	boundary := errors.New("synthetic session boundary")
	for _, tc := range []struct{ name, want string }{
		{ui.DefaultProfile, root},
		{"Garden", ui.ProfileDir(root, "Garden")},
	} {
		var stateDir string
		runner := newUIRunnerWithDiscovery(root, io.Discard,
			func(_ context.Context, cfg proxy.Config, _ cloud.Credentials) (commandSession, error) {
				stateDir = cfg.StateDir
				return nil, boundary
			}, net.Listen, noUIDiscovery, func(context.Context) error { return nil })
		ctx := context.WithValue(ui.WithProfile(t.Context(), root, tc.name), tabFolder{}, tc.name)
		if err := runner(ctx, settings, cloud.Credentials{}, proxy.NewStatus(), func(proxy.Dialer) (func(), bool) { return func() {}, true }); !errors.Is(err, boundary) {
			t.Fatalf("%s: = %v, want its session", tc.name, err)
		}
		if stateDir != tc.want {
			t.Errorf("%s's session folder = %q, want %q", tc.name, stateDir, tc.want)
		}
	}
}

// The UI runner takes its app's port gate: a tab's run started while
// Default's is between its binds binds nothing until Default's has bound
// and published every port, then finds the port held by that tab and
// runs copy-only.
func TestUIRunnerPortGate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	if _, err := ui.CreateProfile(root, "Garden"); err != nil {
		t.Fatal(err)
	}
	withTabs(t, root, new(sync.Mutex))
	ports := freePorts(t, 4700, 445)
	var served []string
	for _, p := range ports {
		served = append(served, strconv.Itoa(int(p.Local)))
	}
	settings := ui.Settings{Version: 1, Bind: "127.0.0.1", Idle: "2m", Ports: ports}
	noTCPState := func(context.Context) error { return nil }
	attach := func(proxy.Dialer) (func(), bool) { return func() {}, true }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Default's window says what its run's status says.
	first := proxy.NewStatus()
	fakeWindowFunc(t, root, func() string {
		if bind, ok := first.Serving(); ok {
			return `"serving_address":"` + bind + `","serving_ports":[` + strings.Join(served, ",") + `]`
		}
		return `"serving_address":"","serving_ports":[]`
	})
	atSecond, bind := make(chan struct{}), make(chan struct{})
	calls := 0
	firstRun := newUIRunnerWithDiscovery(root, io.Discard,
		func(ctx context.Context, _ proxy.Config, _ cloud.Credentials) (commandSession, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, func(network, address string) (net.Listener, error) {
			if calls++; calls == 2 {
				close(atSecond)
				<-bind
			}
			return net.Listen(network, address)
		}, noUIDiscovery, noTCPState)
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- firstRun(ui.WithProfile(ctx, root, ui.DefaultProfile), settings, cloud.Credentials{}, first, attach)
	}()
	<-atSecond

	second := proxy.NewStatus()
	var listens atomic.Int32
	copyOnly := make(chan bool, 1)
	secondRun := newUIRunnerWithDiscovery(ui.ProfileDir(root, "Garden"), io.Discard,
		func(context.Context, proxy.Config, cloud.Credentials) (commandSession, error) {
			_, ok := second.CopyOnly()
			copyOnly <- ok
			return nil, errors.New("synthetic session boundary")
		}, func(network, address string) (net.Listener, error) {
			listens.Add(1)
			return net.Listen(network, address)
		}, noUIDiscovery, noTCPState)
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- secondRun(ui.WithProfile(ctx, root, "Garden"), settings, cloud.Credentials{}, second, attach)
	}()
	time.Sleep(50 * time.Millisecond)
	if n := listens.Load(); n != 0 {
		t.Errorf("Garden's run bound %d ports while Default's was binding", n)
	}
	close(bind)
	err := <-secondDone
	select {
	case ok := <-copyOnly:
		if !ok {
			t.Error("Garden's run isn't copy-only")
		}
	default:
		t.Errorf("Garden's run = %v before its session, not copy-only", err)
	}
	cancel()
	<-firstDone
}
