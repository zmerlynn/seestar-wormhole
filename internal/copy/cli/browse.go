package cli

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
	"github.com/zmerlynn/seestar-wormhole/internal/copy/browse"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/engine"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

type browseOptions struct {
	profile  profileFlag
	endpoint string
	// follow follows a saved telescope by its serial at the endpoint the
	// profile filled.
	follow    *follow
	scan      scanFlag
	listen    string
	addr      netip.AddrPort // set by validate from listen
	open      bool
	selection selectionFlags
	// selector is set by validate from selection.
	selector      engine.Selector
	dest          string
	destSet       bool
	historyDir    string
	historyDirSet bool
}

// NewBrowse returns the browse command of program. It is read-only.
func NewBrowse(program string, load LoadProfile) *Command {
	var o browseOptions
	c := &Command{}
	c.Command = &cobra.Command{
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
--profile default fills --dest, --history-dir and --endpoint from the app's
saved settings where they are not given, and, with a destination,
--exclude, --include and --no-default-excludes from its Copy settings.
While the telescope saved in the app has no folder yet, it fills only
--endpoint, and the page lists without statuses.
` + followHelp + `browse never writes: not to the telescope, not to --dest, not to the state.
Logs and errors go to stderr.
Exit 0 when stopped by Ctrl+C, 1 when it cannot start or keep serving, 2 on
a usage error.`,
		Example: "  " + program + " browse --endpoint 127.0.0.1:4445 --open\n" +
			"  " + program + " browse --endpoint 127.0.0.1:4445 --dest ~/Seestar",
		Args: noArguments,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.profile.fill(cmd.Flags(), load, o.fillSaved); err != nil {
				return err
			}
			if err := o.scan.apply(cmd.Flags(), o.follow); err != nil {
				return err
			}
			o.destSet = o.profile.present(cmd.Flags(), "dest")
			o.historyDirSet = o.profile.present(cmd.Flags(), "history-dir")
			if err := o.validate(); err != nil {
				return err
			}
			for _, name := range []string{"exclude", "no-default-excludes", "include"} {
				if !o.destSet && cmd.Flags().Changed(name) {
					return &diagnostic.OptionError{Option: "--" + name, Rule: "give --dest with it; browse shows what the selection leaves out only with statuses"}
				}
			}
			c.store(true, func(ctx context.Context, out io.Writer, log *slog.Logger) error {
				o.profile.log(log)
				return runBrowse(ctx, o, out, log)
			})
			return nil
		},
	}
	f := c.Flags()
	o.profile.register(f)
	o.profile.statuses = true
	o.scan.register(f)
	f.StringVar(&o.endpoint, "endpoint", "", "SMB host:port of the telescope or a proxy (required)")
	f.StringVar(&o.listen, "listen", "127.0.0.1:0", "IP address and port to serve the page on; port 0 picks a free one")
	f.BoolVar(&o.open, "open", false, "also open the page in the default browser")
	o.selection.register(f)
	f.StringVar(&o.dest, "dest", "", "local mirror folder whose state gives each file a status; never created")
	f.StringVar(&o.historyDir, "history-dir", "", "folder for the copy history of --dest (default: the per-user config folder's seestar-copy)")
	return c
}

// validate checks the flags without touching the network or the disk.
func (o *browseOptions) validate() error {
	if o.endpoint == "" {
		return &diagnostic.OptionError{Option: "--endpoint", Rule: "required; give the SMB host:port"}
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
		return &diagnostic.OptionError{Option: "--dest", Rule: "must not be empty; omit it to browse without a status"}
	}
	if o.historyDirSet && o.historyDir == "" {
		return &diagnostic.OptionError{Option: "--history-dir", Rule: "must not be empty; omit it for the default"}
	}
	if o.historyDirSet && !o.destSet {
		return &diagnostic.OptionError{Option: "--history-dir", Rule: "give --dest with it; browse reads a state file only for a destination"}
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
		return netip.AddrPort{}, &diagnostic.OptionError{Option: "--listen", Rule: "give an IP address and port, such as 127.0.0.1:0"}
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
// The program turns the read-only guard on before it calls this.
func runBrowse(ctx context.Context, o browseOptions, out io.Writer, log *slog.Logger) error {
	var records func() (map[string]state.Record, error)
	if o.destSet {
		stateDir, err := state.HistoryDir(o.historyDir)
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
		return followDial(ctx, o.follow, o.endpoint, log, dial)
	}, log.With("endpoint", o.endpoint), browse.Reporter{})
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
