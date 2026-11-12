package cli

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// noDNS makes the default resolver's lookups fail, without sending any,
// for the rest of the test: its server answers with an error naming the
// server.
func noDNS(t *testing.T) {
	saved := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = saved })
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("resolver 198.51.100.53 unreachable")
	}}
}

// A telescope saved with its serial at a hostname, with scans off, whose
// lookup fails, is named in the error of find, sync, delete and browse,
// with the resolver: the production dials say whose name lookup failed.
func TestSavedHostnameLookupFailureNamesTheHost(t *testing.T) {
	commands := map[string][]string{
		"find":   {"find", "--profile", "default"},
		"sync":   {"sync", "--profile", "default", "--once"},
		"delete": {"delete", "--profile", "default", "--ignore-state", "--include", "*.fit", "--force"},
		"browse": {"browse", "--profile", "default"},
	}
	for name, args := range commands {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(readonly.Reset)
			n := twoTelescopes()
			useLAN(t, n)
			noDNS(t)
			// The production dials, which TestMain replaces.
			useDial(t)
			dial = func(ctx context.Context, endpoint string, log *slog.Logger) (remote.Session, error) {
				return remote.Dial(ctx, endpoint, log)
			}
			dialRemover = func(ctx context.Context, endpoint string, log *slog.Logger) (remote.Remover, error) {
				return remote.DialRemover(ctx, endpoint, log)
			}
			useProfile(t, Saved{Endpoint: "telescope.invalid:445", LANSerial: "SERIAL-A", LANScan: "off", Dest: t.TempDir(), HistoryDir: t.TempDir()})
			var out, errs string
			if name == "browse" {
				b := startBrowseArgs(t, args...)
				b.get(t, "api/list?path=")
				b.end()
				errs = b.stderr.Take()
			} else {
				var code int
				code, out, errs = invoke(t, args...)
				if code == exitOK {
					t.Errorf("exit %d", code)
				}
			}
			for _, named := range []string{"telescope.invalid", "198.51.100.53"} {
				if !strings.Contains(out+errs, named) {
					t.Errorf("output doesn't name %q:\n%s%s", named, out, errs)
				}
			}
			if n.scans() != 0 {
				t.Errorf("scans %d with scans off", n.scans())
			}
		})
	}
}

// A followed telescope that a scan found, at its saved address or at
// another, but that refuses or times out the connect, is named in the
// logs and the error of find, sync, delete and browse: its address, and
// the name, model and serial it answered the scan with.
func TestFollowedConnectFailureNamesTheTelescope(t *testing.T) {
	failures := map[string]*net.OpError{
		"refused":   &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)},
		"timed out": &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded},
	}
	commands := map[string][]string{
		"find":   {"find", "--profile", "default"},
		"sync":   {"sync", "--profile", "default", "--once"},
		"delete": {"delete", "--profile", "default", "--ignore-state", "--include", "*.fit", "--force"},
		"browse": {"browse", "--profile", "default"},
	}
	// The scan finds SERIAL-A at 192.0.2.20: the saved address, or another.
	for where, saved := range map[string]string{"at the saved address": "192.0.2.20:445", "moved": "192.0.2.7:445"} {
		for failure, failed := range failures {
			for name, args := range commands {
				t.Run(where+" "+failure+" "+name, func(t *testing.T) {
					t.Cleanup(readonly.Reset)
					useLAN(t, twoTelescopes())
					useProfile(t, Saved{Endpoint: saved, LANSerial: "SERIAL-A", Dest: t.TempDir(), HistoryDir: t.TempDir()})
					useDial(t)
					var dialed []string
					dial = func(_ context.Context, endpoint string, _ *slog.Logger) (remote.Session, error) {
						dialed = append(dialed, endpoint)
						return nil, &net.OpError{Op: failed.Op, Net: failed.Net, Addr: tcpAddr(endpoint), Err: failed.Err}
					}
					dialRemover = func(_ context.Context, endpoint string, _ *slog.Logger) (remote.Remover, error) {
						dialed = append(dialed, endpoint)
						return nil, &net.OpError{Op: failed.Op, Net: failed.Net, Addr: tcpAddr(endpoint), Err: failed.Err}
					}
					var out, errs string
					if name == "browse" {
						b := startBrowseArgs(t, args...)
						b.get(t, "api/list?path=")
						b.end()
						errs = b.stderr.Take()
					} else {
						var code int
						code, out, errs = invoke(t, args...)
						if code == exitOK {
							t.Errorf("exit %d", code)
						}
					}
					// The scan found SERIAL-A at 192.0.2.20, and only it was dialed.
					if len(dialed) == 0 || dialed[0] != "192.0.2.20:445" {
						t.Errorf("dialed %q", dialed)
					}
					for _, named := range []string{"192.0.2.20:445", "S50_A (Seestar S50, serial SERIAL-A)"} {
						if !strings.Contains(out+errs, named) {
							t.Errorf("output doesn't name %q:\n%s%s", named, out, errs)
						}
					}
				})
			}
		}
	}
}

// A scan that doesn't see the saved serial is logged as such when the
// saved address it falls back to doesn't connect either.
func TestFollowNotSeenFallbackFailureIsLogged(t *testing.T) {
	useLAN(t, &lanNet{})
	var logs bytes.Buffer
	f := newFollow(Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-A"}, none)
	var calls []string
	_, err := followDial(t.Context(), f, f.saved, diagnostic.Logger("text", &logs, slog.LevelInfo), fakeDial(&calls))
	if err == nil || len(calls) != 1 || calls[0] != "192.0.2.7:445" {
		t.Fatalf("err %v, calls %q", err, calls)
	}
	if !strings.Contains(logs.String(), `level=WARN msg="the telescope wasn't seen in a scan of this network; tried its saved address" serial=SERIAL-A saved=192.0.2.7:445 replies=0`) {
		t.Errorf("logs: %s", logs.String())
	}
}

// Another serial answering at the saved address while the saved one
// answers elsewhere, and doesn't connect there, is logged as a sighting
// that didn't connect, not as a telescope the scan didn't find.
func TestFollowConflictWithSightingIsLoggedAsUnreached(t *testing.T) {
	useLAN(t, &lanNet{answers: map[netip.Addr][3]string{
		netip.MustParseAddr("192.0.2.20"): {"SERIAL-B", "Other", "Seestar S30"},
		netip.MustParseAddr("192.0.2.30"): {"SERIAL-A", "S50_A", "Seestar S50"},
	}})
	var logs bytes.Buffer
	f := newFollow(Saved{Endpoint: "192.0.2.20:445", LANSerial: "SERIAL-A"}, none)
	var calls []string
	_, err := followDial(t.Context(), f, f.saved, diagnostic.Logger("text", &logs, slog.LevelInfo), fakeDial(&calls))
	if err == nil || len(calls) != 1 || calls[0] != "192.0.2.30:445" {
		t.Fatalf("err %v, calls %q", err, calls)
	}
	if strings.Contains(logs.String(), "didn't find the saved one") ||
		!strings.Contains(logs.String(), `level=WARN msg="found the telescope on this network, but it didn't connect where it answered" telescope="S50_A (Seestar S50, serial SERIAL-A)" saved=192.0.2.20:445 addresses=[192.0.2.30] other="Other (Seestar S30, serial SERIAL-B)"`) {
		t.Errorf("logs: %s", logs.String())
	}
}

// tcpAddr is endpoint as a dial error names it.
func tcpAddr(endpoint string) net.Addr {
	return net.TCPAddrFromAddrPort(netip.MustParseAddrPort(endpoint))
}
