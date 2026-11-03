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
	"time"
)

// Options is what the app runs with.
type Options struct {
	SettingsPath string // app.json
	StateDir     string // the CLI's default state folder
	Version      string
	Log          *slog.Logger
}

// Run reads the settings, serves the app on a random loopback port and
// calls open with the page's URL, which carries the token in its fragment.
// It serves until ctx is done or open returns. A settings file that can't
// be read is an error before anything is served; its text is the message
// to show.
func Run(ctx context.Context, o Options, open func(ctx context.Context, url string) error) error {
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
	addr := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), uint16(ln.Addr().(*net.TCPAddr).Port))
	s := newServer(ctx, addr, hex.EncodeToString(raw), o.Version, o.StateDir, file, o.Log)
	s.startBrowse()
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          slog.NewLogLogger(o.Log.Handler(), slog.LevelWarn),
	}
	var serveErr error
	served := make(chan struct{})
	go func() {
		serveErr = srv.Serve(ln)
		close(served)
	}()
	o.Log.Info("app serving", "settings", file.path, "listen", addr.String())
	opened := make(chan error, 1)
	go func() { opened <- open(ctx, "http://"+addr.String()+"/app/#"+s.token) }()
	select {
	case err = <-opened:
	case <-served:
		err = fmt.Errorf("serve: %w", serveErr)
	}
	// As browse does: close the sessions first, which ends any SMB call in
	// flight, so the requests waiting on one finish within a second.
	s.close()
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	srv.Shutdown(stop)
	srv.Close()
	ln.Close()
	<-served
	if err == nil {
		o.Log.Info("app stopped")
	}
	return err
}
