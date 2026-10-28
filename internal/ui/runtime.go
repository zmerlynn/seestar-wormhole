package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
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
				return errors.New("UI instance check failed")
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
						return errors.New("UI activation failed")
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
			return errors.New("UI already running or starting; try again shortly")
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
func Run(ctx context.Context, root string, out io.Writer, run runFunc) error {
	return runPlatformUI(ctx, root, out, run)
}

// windowFunc runs on the caller's main thread. Its context ends only after
// proxy cleanup and HTTP shutdown; close requests must call quit and wait.
type windowFunc func(context.Context, string, func(), <-chan struct{}) error

func runUI(ctx context.Context, root string, out io.Writer, run runFunc, open func(string) error, chain keychain) error {
	return runUIWithWindow(ctx, root, out, run, open, chain, nil)
}
func runUIWithWindow(ctx context.Context, root string, out io.Writer, run runFunc, open func(string) error, chain keychain, window windowFunc) error {
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
	acquired, err := tryLock(lock)
	if err != nil {
		return errStorage
	}
	if !acquired {
		return reopen(ctx, st, open, out)
	}
	settings, err := loadSettings(st)
	if os.IsNotExist(err) {
		settings = defaultSettings()
	} else if err != nil {
		return errors.New("Saved UI settings are invalid or unsafe; check config.json in the state directory")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("UI loopback listener unavailable")
	}
	defer listener.Close()
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		return errors.New("UI token unavailable")
	}
	record := runtimeRecord{Port: listener.Addr().(*net.TCPAddr).Port, Token: hex.EncodeToString(token[:])}
	c := &controller{parent: ctx, run: run, credentials: &credentialStore{chain: chain}}
	handler := &server{host: record.host(), token: record.Token, controller: c, storage: st, settings: settings, quit: make(chan struct{})}
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
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	if err = st.write("ui-runtime.json", record); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(root, "ui-runtime.json"))
	defer os.Remove(filepath.Join(root, bootstrapName))
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	windowCtx, windowDone := context.WithCancel(context.Background())
	defer windowDone()
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		select {
		case <-ctx.Done():
		case <-handler.quit:
		case <-done:
		}
		c.quit()
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdown); err != nil {
			httpServer.Close()
		}
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
