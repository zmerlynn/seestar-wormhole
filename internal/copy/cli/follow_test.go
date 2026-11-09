package cli

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

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
