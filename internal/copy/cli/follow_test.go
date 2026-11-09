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
// lookup fails, is named nowhere in the logs or the error of find, sync,
// delete or browse, nor is the resolver: the production dials say the
// saved telescope's name lookup failed.
func TestSavedHostnameLookupFailureNamesNoHost(t *testing.T) {
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
			useDialAs(t)
			dialAs = func(ctx context.Context, name, endpoint string, log *slog.Logger) (remote.Session, error) {
				return remote.DialVia(ctx, name, remote.TCP(endpoint), log)
			}
			dialRemoverAs = func(ctx context.Context, name, endpoint string, log *slog.Logger) (remote.Remover, error) {
				return remote.DialRemoverVia(ctx, name, remote.TCP(endpoint), log)
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
			if !strings.Contains(errs, lanName) || !strings.Contains(errs, "name lookup") {
				t.Errorf("the failure isn't the saved telescope's lookup:\n%s", errs)
			}
			for _, private := range []string{"telescope.invalid", "198.51.100.53", ":53", "SERIAL-"} {
				if strings.Contains(out+errs, private) {
					t.Errorf("output carries %q:\n%s%s", private, out, errs)
				}
			}
			if n.scans() != 0 {
				t.Errorf("scans %d with scans off", n.scans())
			}
		})
	}
}

// A followed telescope that a scan found, at its saved address or at
// another, but that refuses or times out the connect, is named nowhere
// in the logs or the error of find, sync, delete or browse: they call it
// the saved telescope.
func TestFollowedConnectFailureNamesNoAddress(t *testing.T) {
	failures := map[string]error{
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
				// The subtest's name, in its temporary folders, names no address.
				t.Run(where+" "+failure+" "+name, func(t *testing.T) {
					t.Cleanup(readonly.Reset)
					useLAN(t, twoTelescopes())
					useProfile(t, Saved{Endpoint: saved, LANSerial: "SERIAL-A", Dest: t.TempDir(), HistoryDir: t.TempDir()})
					useDialAs(t)
					var dialed []string
					dialAs = func(_ context.Context, label, endpoint string, _ *slog.Logger) (remote.Session, error) {
						dialed = append(dialed, label+" "+endpoint)
						return nil, failed
					}
					dialRemoverAs = func(_ context.Context, label, endpoint string, _ *slog.Logger) (remote.Remover, error) {
						dialed = append(dialed, label+" "+endpoint)
						return nil, failed
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
					if len(dialed) == 0 || dialed[0] != lanName+" 192.0.2.20:445" {
						t.Errorf("dialed %q", dialed)
					}
					if !strings.Contains(errs, lanName) {
						t.Errorf("the failure doesn't name the saved telescope:\n%s", errs)
					}
					for _, private := range []string{"192.0.2.", "SERIAL-", "S50_A"} {
						if strings.Contains(out+errs, private) {
							t.Errorf("output carries %q:\n%s%s", private, out, errs)
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
	var calls, plain, names []string
	_, err := followDial(t.Context(), f, f.saved, diagnostic.Logger("text", &logs, slog.LevelInfo),
		fakeDial(&plain), fakeDialAs(&names, &calls))
	if err == nil || len(calls) != 1 || calls[0] != "192.0.2.7:445" {
		t.Fatalf("err %v, calls %q", err, calls)
	}
	if !strings.Contains(logs.String(), `level=WARN msg="the saved telescope wasn't seen in a scan of this network; tried its saved address"`) {
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
	var calls, plain, names []string
	_, err := followDial(t.Context(), f, f.saved, diagnostic.Logger("text", &logs, slog.LevelInfo),
		fakeDial(&plain), fakeDialAs(&names, &calls))
	if err == nil || len(calls) != 1 || calls[0] != "192.0.2.30:445" {
		t.Fatalf("err %v, calls %q", err, calls)
	}
	if strings.Contains(logs.String(), "didn't find the saved one") ||
		!strings.Contains(logs.String(), `level=WARN msg="found the saved telescope on this network, but it didn't connect where it answered" model="Seestar S50"`) {
		t.Errorf("logs: %s", logs.String())
	}
}
