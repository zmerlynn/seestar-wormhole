package ui

import (
	"context"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/buildinfo"
	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/localhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// runtimeRecord is how a launch or a file command reaches the app that
// has a folder open: its local controls' port and token, and the path of
// the folder's tab, "" for an app of one folder at /.
type runtimeRecord struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	Path  string `json:"path,omitzero"`
}

func (r runtimeRecord) valid() bool {
	b, err := hex.DecodeString(r.Token)
	return err == nil && len(b) == 32 && r.Port > 0 && r.Port <= 65535 && (r.Path == "" || strings.HasPrefix(r.Path, "/p/") && strings.HasSuffix(r.Path, "/"))
}
func (r runtimeRecord) host() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(r.Port)) }

// url is the app's private URL: its shell, or an older app's page.
func (r runtimeRecord) url() string { return "http://" + r.host() + "/#" + r.Token }

// api is the URL of the folder's own API action.
func (r runtimeRecord) api(action string) string {
	path := r.Path
	if path == "" {
		path = "/"
	}
	return "http://" + r.host() + path + "api/" + action
}

// askApp asks the app recorded in the folder of s to show itself, and,
// when profile isn't "", to open or select that profile's tab. An app
// without tabs is asked only when its folder is the one asked for, own.
// done is false while no app answers there; err is an app's refusal.
func askApp(ctx context.Context, client *http.Client, s *storage, profile string, own bool, open func(string) error, out io.Writer) (done bool, err error) {
	var record runtimeRecord
	if err := s.read("ui-runtime.json", &record); err != nil || !record.valid() {
		return false, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+record.host()+"/api/instance", nil)
	if err != nil {
		return true, errors.New("Could not check the running app.")
	}
	req.Header.Set("X-Seestar-Token", record.Token)
	response, err := client.Do(req)
	if err != nil {
		return false, nil
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 128))
	response.Body.Close()
	if readErr != nil || response.StatusCode != 200 || string(body) != `{"instance":"seestar-wormhole-ui-v1"}` {
		return false, nil
	}
	tabs := response.Header.Get("X-Seestar-Tabs") == "1"
	if !tabs && !own {
		return false, nil
	}
	activation := "{}"
	if tabs && profile != "" {
		b, _ := json.Marshal(tabInput{Profile: profile})
		activation = string(b)
	}
	window := response.Header.Get("X-Seestar-Window") == "1"
	if !window && !tabs {
		openPage(s, record.url(), open, out)
		return true, nil
	}
	req, err = http.NewRequestWithContext(ctx, "POST", "http://"+record.host()+"/api/activate", strings.NewReader(activation))
	if err != nil {
		return true, errors.New("Could not reopen the running app.")
	}
	req.Header.Set("X-Seestar-Token", record.Token)
	req.Header.Set("Origin", "http://"+record.host())
	req.Header.Set("Content-Type", "application/json")
	activated, err := client.Do(req)
	if err != nil {
		return false, nil
	}
	defer activated.Body.Close()
	var refusal struct {
		Message string `json:"message"`
	}
	switch {
	case activated.StatusCode == http.StatusOK:
	case activated.StatusCode == http.StatusBadRequest && json.UnmarshalRead(io.LimitReader(activated.Body, localhttp.MaxJSON), &refusal) == nil && refusal.Message != "":
		return true, errors.New(refusal.Message)
	default:
		return false, nil
	}
	if !window {
		openPage(s, record.url(), open, out)
	}
	return true, nil
}

// reopen asks the app starting or running on the folder of s, which
// holds its lock, to show itself and the profile's tab, as askApp does,
// for as long as it takes to start. held, if set, is the refusal when
// no app answers in time.
func reopen(ctx context.Context, s *storage, profile string, open func(string) error, out io.Writer, held error) error {
	client := probeClient()
	defer client.CloseIdleConnections()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if done, err := askApp(ctx, client, s, profile, true, open, out); done {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			if held != nil {
				return held
			}
			return errors.New("App is already running or starting. Try again shortly.")
		case <-tick.C:
		}
	}
}

// askRunning asks an app already running with one of root's profiles open
// to show itself, and the profile's tab when the launch named one:
// first the app of that profile's folder, then any app with tabs.
func askRunning(ctx context.Context, root, name, profile string, open func(string) error, out io.Writer) (bool, error) {
	names, _ := Profiles(root)
	client := probeClient()
	defer client.CloseIdleConnections()
	for i, n := range append([]string{name, DefaultProfile}, names...) {
		if i > 0 && sameProfile(n, name) {
			continue
		}
		st := &storage{root: ProfileDir(root, n)}
		if done, err := askApp(ctx, client, st, profile, i == 0, open, out); done {
			return true, err
		}
	}
	return false, nil
}

const bootstrapName = "ui-open.html"

func openPage(st *storage, url string, open func(string) error, out io.Writer) {
	// Pass only a private filename to xdg-open, open, or ShellExecute. ShellExecute
	// can hand its target to a browser process too, so all platforms use this file.
	target, _ := json.Marshal(url)
	html := []byte(`<!doctype html><meta name="referrer" content="no-referrer"><script>location.replace(` + string(target) + `)</script>`)
	err := st.writeBytes(bootstrapName, html)
	if err == nil {
		var path string
		path, err = filepath.Abs(filepath.Join(st.root, bootstrapName))
		if err == nil {
			err = open(path)
		}
	}
	if err != nil {
		fmt.Fprintln(out, "Open this private URL in your browser:", url)
	}
}

// Run serves one UI per private state root. It never starts a proxy on load.
func StateRoot(root string) (string, error) {
	if root != "" {
		return root, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", storageFailed(err)
	}
	return filepath.Join(base, "seestar-wormhole"), nil
}

func uiVersion(ctx context.Context) string {
	version, _ := ctx.Value(versionKey{}).(string)
	return buildinfo.Version(version)
}

type versionKey struct{}

// Run runs the UI. out gets its messages for the person who started it,
// and logs, the command's bounded stderr sink, its diagnostics.
func Run(ctx context.Context, root string, out, logs io.Writer, run runFunc, version string) error {
	return runPlatformUI(context.WithValue(ctx, versionKey{}, version), root, out, logs, run)
}

// newHTTPServer is the local controls' server. Its write timeout bounds
// every response but Browse's, which clears it for a download.
func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
}

// windowFunc runs on the caller's main thread. Its context ends only after
// proxy cleanup and HTTP shutdown; close requests must call quit and wait.
// The window's dialogs, passed beside it to runUIPresented, serve copy's
// folder picker and Browse's Save; nil means a browser page.
type windowFunc func(context.Context, string, func(), <-chan struct{}) error

// copyDial, if set, is how the copier reaches the telescope by any
// route. Only the native check's guitest build and tests set it, to a
// fake telescope.
var copyDial func(ctx context.Context, t copyapp.Target) (remote.Session, error)

// The copier's transports, by route: over TCP on this network, and over a
// channel of the run's session through the account. Only tests replace
// them. A failed dial returns a nil interface, never a nil pointer in one.
var (
	tcpDial = func(ctx context.Context, endpoint string, log *slog.Logger) (remote.Session, error) {
		s, err := remote.Dial(ctx, endpoint, log)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	tcpRemover = func(ctx context.Context, endpoint string, log *slog.Logger) (remote.Remover, error) {
		s, err := remote.DialRemover(ctx, endpoint, log)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	dialVia = func(ctx context.Context, name string, conn remote.Conn, log *slog.Logger) (remote.Session, error) {
		s, err := remote.DialVia(ctx, name, conn, log)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	removerVia = func(ctx context.Context, name string, conn remote.Conn, log *slog.Logger) (remote.Remover, error) {
		s, err := remote.DialRemoverVia(ctx, name, conn, log)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
)

// copyMountOptions completes o, the copier's settings and environment,
// with the host's target and dial hooks: it is the one builder of the
// app's MountOptions. The hooks choose the route by the target's Run,
// never by its endpoint's text: Run 0 dials the endpoint over TCP, and
// any other run dials device port 445 through that run's session. On this
// network a target saved with its serial is reached as the saved
// LANScan says (see [lanConnect]), except by a delete's remover, which
// dials the exact target its preview reached. They look the run up on each
// dial, so one Mount reaches whichever run is current. Every file-tool
// outcome goes to the app's event trail.
func copyMountOptions(handler *server, o copyapp.MountOptions) copyapp.MountOptions {
	files := &handler.controller.files
	via := func(run uint64) remote.Conn {
		return func(ctx context.Context) (net.Conn, error) { return files.dial(ctx, run) }
	}
	log := o.Log
	follow := func(ctx context.Context, t copyapp.Target) (remote.Session, copyapp.Target, error) {
		return lanConnect(ctx, handler, t, func(ctx context.Context, endpoint string) (remote.Session, error) {
			return tcpDial(ctx, endpoint, log)
		})
	}
	o.Target = handler.copyTarget
	o.Name = handler.telescopeName
	o.KnowSerial = handler.knowSerial
	o.SerialAt = handler.serialAt
	o.Report = handler.controller.log.addOutcome
	o.Dial = func(ctx context.Context, t copyapp.Target) (remote.Session, error) {
		switch {
		case copyDial != nil:
			return copyDial(ctx, t)
		case t.Run == 0 && t.Serial != "":
			s, _, err := follow(ctx, t)
			return s, err
		case t.Run == 0:
			return tcpDial(ctx, t.Endpoint, log)
		}
		return dialVia(ctx, t.Endpoint, via(t.Run), log)
	}
	o.Resolve = func(ctx context.Context, t copyapp.Target) (remote.Session, copyapp.Target, error) {
		if copyDial == nil && t.Run == 0 && t.Serial != "" {
			return follow(ctx, t)
		}
		s, err := o.Dial(ctx, t)
		return s, t, err
	}
	o.Remover = func(ctx context.Context, t copyapp.Target) (remote.Remover, error) {
		if t.Run == 0 {
			return tcpRemover(ctx, t.Endpoint, log)
		}
		return removerVia(ctx, t.Endpoint, via(t.Run), log)
	}
	return o
}

// newMount is the app's one Mount call, at startup; tests count it.
var newMount = copyapp.NewMount

// copyRefusedEvent is Activity's line for a copy.json this version
// refused: one fixed text, with no path and no error.
const copyRefusedEvent = "Copy settings couldn't be loaded safely; Copy, Browse and Free up space are off."

// quitCopy stops and joins the copier, if this run mounted one.
func quitCopy(handler *server) {
	if handler.copy != nil {
		handler.copy.Quit()
	}
}

// runText reads a run's status text for the status's Ready; nil is its
// Text. Only tests set it, since only a proxy session makes a status
// Ready.
var runText func(*proxy.Status) string

// copyTargetHook, if set, runs as each copy target is read from its
// snapshots, after they were taken. Only tests set it, to hold a lookup.
var copyTargetHook func()

// runUIPresented runs the app: on one folder, or, when ctx is of a
// profile, with tabs of root's profiles, the asked one open first. A
// launch while an app has that folder open, or has tabs of these
// profiles, asks it instead.
func runUIPresented(ctx context.Context, root string, out, logs io.Writer, run runFunc, open func(string) error, chain keychain, window windowFunc, native *copyapp.Native) (result error) {
	name, asked, profiles := DefaultProfile, "", false
	if p, ok := profileOf(ctx); ok {
		root, asked, profiles = p.root, p.name, true
		if asked != "" {
			name = asked
		}
		if done, err := askRunning(ctx, root, name, asked, open, out); done {
			return err
		}
	}
	// The proxies and the copiers stop only by the cleanup below, in its
	// order, so the end of ctx can't reach a proxy before Browse and its
	// copier have stopped.
	base, baseDone := context.WithCancel(context.WithoutCancel(ctx))
	defer baseDone()
	a, listener, err := newApp(ctx, base, root, profiles, logs, run, chain, native, window != nil)
	if err != nil {
		return err
	}
	defer listener.Close()
	first, err := a.openTab(name, "")
	if errors.Is(err, errLocked) {
		st, err := openStorage(a.dir(name))
		if err != nil {
			return err
		}
		defer st.directory.Close()
		// A profile's folder that no app answers for is held by another
		// process: its lock says so.
		var held error
		if profiles {
			held = errors.New("Couldn't open " + a.label(name) + ": " + lockedError(a.label(name), a.dir(name)).Error() + ".")
		}
		return reopen(ctx, st, asked, open, out, held)
	} else if err != nil {
		return err
	}
	// Every return stops each tab's copier and joins it before its lock
	// is released; the cleanup below has usually done so already.
	defer a.closeAll()
	activate := make(chan struct{}, 1)
	if window != nil {
		a.activate = func() {
			select {
			case activate <- struct{}{}:
			default:
			}
		}
	}
	httpServer := newHTTPServer(a)
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	shell := &windowShell{live: func(mark time.Time) bool { return a.live(mark, 2*time.Second) }, confirm: a.confirmQuit}
	windowCtx, windowDone := context.WithCancel(context.WithValue(context.WithValue(context.Background(), versionKey{}, a.version), windowShellKey{}, shell))
	defer windowDone()
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		select {
		case <-ctx.Done():
		case <-a.quit:
		case <-done:
		}
		a.quitting.Store(true)
		// Native termination (notably AppKit) may exit without returning
		// Run. Each tab removes its capability files as it closes, before
		// OnShutdown may complete.
		a.closeAll()
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			httpServer.Close()
		}
		windowDone()
	}()
	if window == nil {
		openPage(first.st, a.url(), open, out)
	} else {
		err = window(windowCtx, a.url(), a.requestQuit, activate)
		a.requestQuit() // Also joins cleanup if native startup fails.
	}
	<-cleanupDone
	return err
}
