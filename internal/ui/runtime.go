package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/buildinfo"
	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/filelock"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

type runtimeRecord struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

func (r runtimeRecord) valid() bool {
	b, err := hex.DecodeString(r.Token)
	return err == nil && len(b) == 32 && r.Port > 0 && r.Port <= 65535
}
func (r runtimeRecord) host() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(r.Port)) }
func (r runtimeRecord) url() string  { return "http://" + r.host() + "/#" + r.Token }
func reopen(ctx context.Context, s *storage, open func(string) error, out io.Writer) error {
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, Timeout: 300 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		var record runtimeRecord
		if err := s.read("ui-runtime.json", &record); err == nil && record.valid() {
			req, err := http.NewRequestWithContext(ctx, "GET", "http://"+record.host()+"/api/instance", nil)
			if err != nil {
				return errors.New("Could not check the running app.")
			}
			req.Header.Set("X-Seestar-Token", record.Token)
			response, err := client.Do(req)
			if err == nil {
				body, readErr := io.ReadAll(io.LimitReader(response.Body, 128))
				response.Body.Close()
				if readErr == nil && response.StatusCode == 200 && string(body) == `{"instance":"seestar-wormhole-ui-v1"}` {
					if response.Header.Get("X-Seestar-Window") != "1" {
						openPage(s, record.url(), open, out)
						return nil
					}
					req, err := http.NewRequestWithContext(ctx, "POST", "http://"+record.host()+"/api/activate", strings.NewReader("{}"))
					if err != nil {
						return errors.New("Could not reopen the running app.")
					}
					req.Header.Set("X-Seestar-Token", record.Token)
					req.Header.Set("Origin", "http://"+record.host())
					req.Header.Set("Content-Type", "application/json")
					activated, err := client.Do(req)
					if err == nil {
						io.Copy(io.Discard, io.LimitReader(activated.Body, 128))
						activated.Body.Close()
						if activated.StatusCode == http.StatusOK {
							return nil
						}
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			return errors.New("App is already running or starting. Try again shortly.")
		case <-tick.C:
		}
	}
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
		return "", errStorage
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
	// tcpDialAs and tcpRemoverAs dial endpoint over TCP for a telescope
	// saved with its serial; their errors call it name, never its address.
	tcpDialAs = func(ctx context.Context, name, endpoint string, log *slog.Logger) (remote.Session, error) {
		s, err := remote.DialVia(ctx, name, remote.TCP(endpoint), log)
		if err != nil {
			return nil, err
		}
		return s, nil
	}
	tcpRemoverAs = func(ctx context.Context, name, endpoint string, log *slog.Logger) (remote.Remover, error) {
		s, err := remote.DialRemoverVia(ctx, name, remote.TCP(endpoint), log)
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
// network a target saved with its serial is dialed where a scan hears
// that serial (see [lanFollow]), except by a delete's remover, which dials
// the exact target its preview reached. They look the run up on each
// dial, so one Mount reaches whichever run is current. Every file-tool
// outcome goes to the app's event trail.
func copyMountOptions(handler *server, o copyapp.MountOptions) copyapp.MountOptions {
	files := &handler.controller.files
	via := func(run uint64) remote.Conn {
		return func(ctx context.Context) (net.Conn, error) { return files.dial(ctx, run) }
	}
	log := o.Log
	follow := func(ctx context.Context, t copyapp.Target) (remote.Session, copyapp.Target, error) {
		return lanFollow(ctx, handler, t, func(ctx context.Context, endpoint string) (remote.Session, error) {
			return tcpDialAs(ctx, lanName, endpoint, log)
		})
	}
	o.Target = handler.copyTarget
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
		switch {
		case t.Run == 0 && t.Serial != "":
			return tcpRemoverAs(ctx, lanName, t.Endpoint, log)
		case t.Run == 0:
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

// copyTargetHook, if set, runs at the start of each copy target lookup.
// Only tests set it, to hold a lookup.
var copyTargetHook func()

func runUIPresented(ctx context.Context, root string, out, logs io.Writer, run runFunc, open func(string) error, chain keychain, window windowFunc, native *copyapp.Native) (result error) {
	st, err := openStorage(root)
	if err != nil {
		return err
	}
	defer st.directory.Close()
	lock, err := privatefile.Open(filepath.Join(root, "ui.lock"), os.O_CREATE|os.O_RDWR)
	if err != nil {
		return errStorage
	}
	defer lock.Close()
	acquired, err := filelock.TryLock(lock)
	if err != nil {
		return errStorage
	}
	if !acquired {
		return reopen(ctx, st, open, out)
	}
	eventHistory := &eventLog{storage: st}
	eventHistory.add("App opened.")
	defer func() {
		if result != nil {
			eventHistory.add("App could not start.")
		}
	}()
	unsafeSettings := func(name string) error {
		settingsPath := filepath.Join(root, name)
		if absolute, err := filepath.Abs(settingsPath); err == nil {
			settingsPath = absolute
		}
		return errors.New("Could not load saved settings safely.\n\n" + settingsPath + "\n\nCheck this file, then reopen seestar-wormhole.")
	}
	settings, err := loadSettings(st)
	routeSaved := err == nil && routeSavedIn(settings)
	if os.IsNotExist(err) {
		settings = defaultSettings()
	} else if err != nil {
		return unsafeSettings("config.json")
	}
	// A copy.json this version can't read leaves Copy, Browse and Free
	// up space off for this run and is never written; the proxy needs
	// none of it.
	copyFile, copySaved, err := loadCopySettings(st)
	copyRefused := err != nil
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("Could not start the local controls.")
	}
	defer listener.Close()
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		return errors.New("Could not secure the local controls.")
	}
	record := runtimeRecord{Port: listener.Addr().(*net.TCPAddr).Port, Token: hex.EncodeToString(token[:])}
	// The proxy and the copier stop only by the cleanup below, in its
	// order, so the end of ctx can't reach the proxy before Browse and
	// the copier have stopped.
	appCtx, appDone := context.WithCancel(context.WithoutCancel(ctx))
	defer appDone()
	c := &controller{parent: appCtx, run: run, text: runText, credentials: &credentialStore{chain: chain, savedEmail: func() string {
		if settings.Remember {
			return settings.Email
		}
		return ""
	}()}}
	version := uiVersion(ctx)
	handler := &server{version: version, host: record.host(), token: record.Token, controller: c, storage: st, settings: settings, quit: make(chan struct{})}
	handler.native = native != nil
	handler.routeSaved = routeSaved
	handler.copyRefused = copyRefused
	c.save = handler.saveSettings
	c.lastSaved = settings
	c.log = eventHistory
	// The copier shares the file commands' default history folder, so the
	// two share one history and lock per folder. Its warnings and errors go
	// to the command's bounded log sink, as the proxy's do.
	if copyRefused {
		eventHistory.add(copyRefusedEvent)
	} else {
		copyState, _ := state.DefaultDir()
		listening := listener.Addr().(*net.TCPAddr).AddrPort()
		handler.copy = newMount(appCtx, copyMountOptions(handler, copyapp.MountOptions{
			Addr: netip.AddrPortFrom(listening.Addr().Unmap(), listening.Port()), StateDir: copyState, Version: version,
			Log: diagnostic.Logger("json", logs, slog.LevelWarn), Settings: copySaved, Save: copyFile.save, Native: native,
		}))
	}
	// Every return stops the copier and joins it before ui.lock is
	// released; the cleanup below has usually done so already.
	defer quitCopy(handler)
	activate := make(chan struct{}, 1)
	if window != nil {
		handler.activate = func() {
			select {
			case activate <- struct{}{}:
			default:
			}
		}
	}
	requestQuit := func() { handler.quitOnce.Do(func() { close(handler.quit) }) }
	httpServer := newHTTPServer(handler)
	if err = st.write("ui-runtime.json", record); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(root, "ui-runtime.json"))
	defer os.Remove(filepath.Join(root, bootstrapName))
	// Copying may start as the app opens only once it is published.
	if handler.copy != nil {
		handler.copy.Start()
	}
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	windowCtx, windowDone := context.WithCancel(context.WithValue(context.Background(), versionKey{}, version))
	defer windowDone()
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		select {
		case <-ctx.Done():
		case <-handler.quit:
		case <-done:
		}
		// Browse's sessions and the copier stop before the proxy they
		// may be reaching the telescope through.
		quitCopy(handler)
		c.quit()
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			httpServer.Close()
		}
		// Native termination (notably AppKit) may exit without returning Run.
		// Remove capability files before allowing OnShutdown to complete.
		os.Remove(filepath.Join(root, "ui-runtime.json"))
		os.Remove(filepath.Join(root, bootstrapName))
		windowDone()
	}()
	if window == nil {
		openPage(st, record.url(), open, out)
	} else {
		err = window(windowCtx, record.url(), requestQuit, activate)
		requestQuit() // Also joins cleanup if native startup fails.
	}
	<-cleanupDone
	return err
}
