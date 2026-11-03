package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"
)

// Options is what the app runs with.
type Options struct {
	SettingsPath string // app.json
	StateDir     string // the CLI's default state folder
	Version      string
	Log          *slog.Logger
	Native       *Native // the window's dialogs; nil without a window
}

// Native is the window's own dialogs. Each may be called from any
// goroutine and blocks until the user answers.
type Native struct {
	// ChooseFolder shows a folder picker that starts in dir, if dir is
	// not empty, and returns the folder chosen, or "" on Cancel.
	ChooseFolder func(dir string) (string, error)
	// SaveFile shows a save dialog for a file named name and returns the
	// path chosen, or "" on Cancel.
	SaveFile func(name string) (string, error)
	// Alert shows an error message.
	Alert func(message string)
}

// Run reads the settings, serves the app on a random loopback port and
// calls open, on the caller's goroutine, with the page's URL, which
// carries the token in its fragment. quit asks the app to stop. The
// context open gets is done only once the app has stopped serving, after
// ctx is done, quit is called or serving fails; open should then return.
// Run returns once open has. A settings file that can't be read is an
// error before anything is served; its text is the message to show.
func Run(ctx context.Context, o Options, open func(ctx context.Context, url string, quit func()) error) error {
	file, err := loadSettings(o.SettingsPath)
	if err != nil {
		return err
	}
	raw := make([]byte, 32)
	rand.Read(raw)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	running, stopRunning := context.WithCancel(ctx)
	defer stopRunning()
	addr := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), uint16(ln.Addr().(*net.TCPAddr).Port))
	s := newServer(running, addr, hex.EncodeToString(raw), o.Version, o.StateDir, file, o.Log)
	s.native = o.Native
	s.startBrowse()
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
		BaseContext:       func(net.Listener) context.Context { return running },
		ErrorLog:          slog.NewLogLogger(o.Log.Handler(), slog.LevelWarn),
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	o.Log.Info("app serving", "settings", file.path, "listen", addr.String())

	quitting := make(chan struct{})
	quit := sync.OnceFunc(func() { close(quitting) })
	// The window's context outlives ctx until the server has stopped, so a
	// window closing at logout waits for the cleanup.
	window, windowDone := context.WithCancel(context.WithoutCancel(ctx))
	stopped := make(chan error, 1)
	go func() {
		defer windowDone()
		var err error
		select {
		case <-ctx.Done():
		case <-quitting:
		case err = <-served:
			err = fmt.Errorf("serve: %w", err)
		}
		// As browse does: end the requests' context and close the sessions
		// first, which ends any SMB call in flight, so the requests
		// waiting on one finish within a second.
		stopRunning()
		s.close()
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		srv.Shutdown(stop)
		cancel()
		srv.Close()
		ln.Close()
		if err == nil {
			<-served
		}
		stopped <- err
	}()
	err = open(window, "http://"+addr.String()+"/app/#"+s.token, quit)
	quit()
	if serveErr := <-stopped; err == nil {
		err = serveErr
	}
	if err == nil {
		o.Log.Info("app stopped")
	}
	return err
}
