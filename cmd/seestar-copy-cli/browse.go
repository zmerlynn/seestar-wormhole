package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/spf13/cobra"
	"github.com/zmerlynn/seestar-copy/internal/browse"
	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

type browseOptions struct {
	endpoint  string
	listen    string
	addr      netip.AddrPort // set by validate from listen
	open      bool
	selection selectionFlags
	// selector is set by validate from selection.
	selector    engine.Selector
	dest        string
	destSet     bool
	stateDir    string
	stateDirSet bool
}

func newBrowseCommand(log *slog.Logger) *cobra.Command {
	var o browseOptions
	cmd := &cobra.Command{
		Use:   "browse --endpoint HOST:PORT [flags]",
		Short: "Serve a read-only web page for looking through the telescope's share",
		Long: `Serve a read-only web page for looking through the telescope's share.
The page lists one folder at a time and saves any file to this computer
through the browser. Clicking a .jpg shows the image, a .txt its text, and a
.fit or .fits its FITS header as text. It shows the whole share.
With --dest, each file shows its status in that destination's state, as
for find --dest: copied, changed or new, or left out for a file or folder
that the selection leaves out. --exclude, --no-default-excludes and
--include select exactly as for sync, and need --dest.
Once listening, browse prints one line to stdout, the page's URL, and
serves until Ctrl+C. --open also opens that URL in the default browser.
--listen takes an IP address and a port, never a host name; port 0 picks a
free port. An IPv4-mapped IPv6 address, such as [::ffff:192.168.1.5]:0, and
an address with a zone, such as [fe80::1%en0]:0, are refused. Use the plain
IPv4 address for a mapped address. An IPv6 address with a zone is not
supported; choose another address, such as [::1]:0 or an available IPv4
address. Loopback addresses, such as the default, serve this computer only.
A non-loopback address, including 0.0.0.0 or ::, serves the share without a
password to anyone who can reach it, and browse logs a warning when it
starts.
browse never writes: not to the telescope, not to --dest, not to the state.
Logs and errors go to stderr.
Exit 0 when stopped by Ctrl+C, 1 when it cannot start or keep serving, 2 on
a usage error.`,
		Example: "  " + programName + " browse --endpoint 127.0.0.1:4445 --open\n" +
			"  " + programName + " browse --endpoint 127.0.0.1:4445 --dest ~/Seestar",
		Args: noArguments,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.destSet = cmd.Flags().Changed("dest")
			o.stateDirSet = cmd.Flags().Changed("state-dir")
			if err := o.validate(); err != nil {
				return err
			}
			for _, name := range []string{"exclude", "no-default-excludes", "include"} {
				if !o.destSet && cmd.Flags().Changed(name) {
					return &usageError{Option: "--" + name, Rule: "give --dest with it; browse shows what the selection leaves out only with statuses"}
				}
			}
			return runBrowse(cmd.Context(), o, cmd.OutOrStdout(), log)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.endpoint, "endpoint", "", "SMB host:port of the telescope or a proxy (required)")
	f.StringVar(&o.listen, "listen", "127.0.0.1:0", "IP address and port to serve the page on; port 0 picks a free one")
	f.BoolVar(&o.open, "open", false, "also open the page in the default browser")
	o.selection.register(f)
	f.StringVar(&o.dest, "dest", "", "local mirror folder whose state gives each file a status; never created")
	f.StringVar(&o.stateDir, "state-dir", "", "folder for the state file of --dest (default: the per-user config folder's seestar-copy)")
	return cmd
}

// validate checks the flags without touching the network or the disk.
func (o *browseOptions) validate() error {
	if o.endpoint == "" {
		return &usageError{Option: "--endpoint", Rule: "required; give the SMB host:port"}
	}
	if err := validateEndpoint(o.endpoint); err != nil {
		return err
	}
	addr, err := parseListen(o.listen)
	if err != nil {
		return err
	}
	o.addr = addr
	if o.destSet && o.dest == "" {
		return &usageError{Option: "--dest", Rule: "must not be empty; omit it to browse without a status"}
	}
	if o.stateDirSet && o.stateDir == "" {
		return &usageError{Option: "--state-dir", Rule: "must not be empty; omit it for the default"}
	}
	if o.stateDirSet && !o.destSet {
		return &usageError{Option: "--state-dir", Rule: "give --dest with it; browse reads a state file only for a destination"}
	}
	sel, err := o.selection.parse()
	if err != nil {
		return err
	}
	o.selector = sel
	return nil
}

// parseListen accepts an IP literal and a port. A host name, an
// IPv4-mapped IPv6 address and a zone are refused: the handler's Host
// check compares IP literals, and a browser has no single spelling for the
// last two.
func parseListen(s string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil || ap.Addr().Is4In6() || ap.Addr().Zone() != "" {
		return netip.AddrPort{}, &usageError{Option: "--listen", Rule: "give an IP address and port, such as 127.0.0.1:0"}
	}
	return ap, nil
}

// pageURL is the URL browse prints and opens. A wildcard address prints the
// loopback address of its family.
func pageURL(addr netip.AddrPort) string {
	ip := addr.Addr()
	switch {
	case ip == netip.IPv4Unspecified():
		ip = netip.AddrFrom4([4]byte{127, 0, 0, 1})
	case ip == netip.IPv6Unspecified():
		ip = netip.IPv6Loopback()
	}
	return "http://" + netip.AddrPortFrom(ip, addr.Port()).String() + "/"
}

// exposed reports whether a listener on ip serves other computers.
func exposed(ip netip.Addr) bool { return !ip.IsLoopback() }

// openBrowser opens a URL in the default browser without waiting for it,
// and logs a failure it sees. Tests replace it.
var openBrowser = openURL

// warnOpen logs that the browser could not be opened.
func warnOpen(log *slog.Logger, err error) {
	log.Warn("could not open a browser", "error", err)
}

// runBrowse serves the page until ctx is done. A stop by ctx returns nil.
func runBrowse(ctx context.Context, o browseOptions, out io.Writer, log *slog.Logger) error {
	readonly.Enable()
	var records func() (map[string]state.Record, error)
	if o.destSet {
		stateDir, err := resolveStateDir(o.stateDir, o.stateDirSet)
		if err != nil {
			return err
		}
		// Read once now, so a bad state file stops browse before it serves.
		if _, err := state.Load(stateDir, o.dest, log); err != nil {
			return err
		}
		file, err := state.Path(stateDir, o.dest)
		if err != nil {
			return err
		}
		records = browse.CacheRecords(file, func() (map[string]state.Record, error) { return state.Load(stateDir, o.dest, log) })
	}
	ln, err := net.Listen("tcp", o.addr.String())
	if err != nil {
		return err
	}
	addr := netip.AddrPortFrom(o.addr.Addr(), uint16(ln.Addr().(*net.TCPAddr).Port))
	sessions := browse.NewSessions(ctx, func(ctx context.Context) (remote.Session, error) {
		return dial(ctx, o.endpoint)
	}, log.With("endpoint", o.endpoint))
	srv := &http.Server{
		Handler: browse.NewHandler(browse.Config{
			Addr: addr, Endpoint: o.endpoint, Sessions: sessions, Records: records, Selector: o.selector, Log: log,
		}),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	var serveErr error
	served := make(chan struct{})
	go func() {
		serveErr = srv.Serve(ln)
		close(served)
	}()
	err = announce(o, addr, out, log)
	if err == nil {
		select {
		case <-ctx.Done():
		case <-served:
			err = fmt.Errorf("serve: %w", serveErr)
		}
	}
	// Close the sessions first, which ends any SMB call in flight, so the
	// requests waiting on one can finish within Shutdown's second.
	sessions.Close()
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	srv.Shutdown(stop)
	srv.Close()
	// Serve may not have started yet, so Shutdown may not have closed the
	// listener: close it here, and wait for Serve, so the port is free on
	// return.
	ln.Close()
	<-served
	if err == nil {
		log.Info("browse stopped")
	}
	return err
}

// announce logs that browse is listening, prints the page's URL and, with
// --open, opens it.
func announce(o browseOptions, addr netip.AddrPort, out io.Writer, log *slog.Logger) error {
	args := []any{"addr", addr.String(), "endpoint", o.endpoint}
	if o.destSet {
		args = append(args, "dest", o.dest)
	}
	log.Info("browse listening", args...)
	if exposed(addr.Addr()) {
		log.Warn("serving the share unauthenticated to the network", "addr", addr.String())
	}
	url := pageURL(addr)
	if _, err := fmt.Fprintln(out, url); err != nil {
		return fmt.Errorf("print the URL: %w", err)
	}
	if o.open {
		openBrowser(url, log)
	}
	return nil
}
