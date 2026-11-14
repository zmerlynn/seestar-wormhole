package cli

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// otherNamed is how errors and logs name both telescopes in the owner's
// setup.
const otherNamed = "another telescope, Relay (S50 Pro, serial TEST0002), answered at 192.0.2.85 instead of serial SERIAL-A"

// errDialed is a dial that should not have been made.
var errDialed = errors.New("dialed")

// useOtherTelescope is the owner's setup of 2026-11-14: the app saved the
// telescope as SERIAL-A at 192.0.2.85:4445, where only TEST0002 answers
// the scan. It returns the endpoints dialed.
func useOtherTelescope(t *testing.T) *[]string {
	t.Helper()
	useLAN(t, &lanNet{answers: map[netip.Addr][3]string{netip.MustParseAddr("192.0.2.85"): {"TEST0002", "Relay", "S50 Pro"}}})
	useProfile(t, Saved{Endpoint: "192.0.2.85:4445", LANSerial: "SERIAL-A", Dest: t.TempDir(), Once: true})
	useDial(t)
	var calls []string
	dial = func(ctx context.Context, endpoint string, log *slog.Logger) (remote.Session, error) {
		calls = append(calls, endpoint)
		return nil, errDialed
	}
	dialRemover = func(ctx context.Context, endpoint string, log *slog.Logger) (remote.Remover, error) {
		calls = append(calls, endpoint)
		return nil, errDialed
	}
	return &calls
}

// sync, find and delete with --profile default fail naming both
// telescopes, and dial nothing.
func TestOtherTelescopeCommands(t *testing.T) {
	t.Cleanup(readonly.Reset)
	for _, args := range [][]string{
		{"sync", "--profile", "default"},
		{"find", "--profile", "default"},
		{"delete", "--profile", "default", "--ignore-state", "--include", "*.fit"},
		{"delete", "--profile", "default", "--ignore-state", "--include", "*.fit", "--force"},
	} {
		readonly.Reset()
		calls := useOtherTelescope(t)
		code, _, errs := invoke(t, args...)
		if code == exitOK || *calls != nil || !strings.Contains(errs, otherNamed) {
			t.Errorf("%q: exit %d, calls %q\n%s", args, code, *calls, errs)
		}
	}
}

// browse with --profile default says in its listing's message, and logs,
// that another telescope answered, naming both.
func TestOtherTelescopeBrowse(t *testing.T) {
	calls := useOtherTelescope(t)
	b := startBrowseArgs(t, "browse", "--profile", "default")
	if code, body := b.get(t, "api/list"); code == 200 || !strings.Contains(body, otherNamed) {
		t.Errorf("listing = %d %s", code, body)
	}
	b.end()
	if errs := b.stderr.Take(); *calls != nil || !strings.Contains(errs, otherNamed) {
		t.Errorf("calls %q\n%s", *calls, errs)
	}
}
