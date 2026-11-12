package cli

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
)

// modeDial is followDial for a test of f's mode: it reports what was
// dialed and the scans so far.
func modeDial(t *testing.T, n *lanNet, f *follow, log *slog.Logger, up ...string) (fakeSession, []string, int, error) {
	t.Helper()
	var calls []string
	got, err := followDial(t.Context(), f, f.saved, log, fakeDial(&calls, up...))
	return got, calls, n.scans(), err
}

// start scans at a run's first connect only, reuses where it connected,
// and scans once more when a connect there fails.
func TestFollowDialStartScansOncePerRun(t *testing.T) {
	n := twoTelescopes()
	useLAN(t, n)
	var logs bytes.Buffer
	log := diagnostic.Logger("text", &logs, slog.LevelInfo)
	for _, saved := range []string{"", "start", "a newer mode"} {
		logs.Reset()
		before := n.scans()
		f := newFollow(Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-A", LANScan: saved}, none)
		// The first connect scans and finds the telescope moved.
		got, calls, scans, err := modeDial(t, n, f, log, "192.0.2.20:445", "192.0.2.21:445")
		if err != nil || got != "session at 192.0.2.20:445" || !slices.Equal(calls, []string{"192.0.2.20:445"}) || scans != before+1 {
			t.Fatalf("saved %q, first connect = %q, %v; calls %q scans %d", saved, got, err, calls, scans-before)
		}
		// Later connects, a long sync's passes, dial there unscanned.
		for range 3 {
			got, calls, scans, err = modeDial(t, n, f, log, "192.0.2.20:445", "192.0.2.21:445")
			if err != nil || got != "session at 192.0.2.20:445" || !slices.Equal(calls, []string{"192.0.2.20:445"}) || scans != before+1 {
				t.Fatalf("saved %q, a later connect = %q, %v; calls %q scans %d", saved, got, err, calls, scans-before)
			}
		}
		if strings.Count(logs.String(), "found the telescope at another address") != 1 {
			t.Errorf("logs: %s", logs.String())
		}
	}
}

func TestFollowDialStartRescansAfterAFailedConnect(t *testing.T) {
	n := twoTelescopes()
	useLAN(t, n)
	var logs bytes.Buffer
	log := diagnostic.Logger("text", &logs, slog.LevelInfo)
	f := newFollow(Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-A"}, none)
	if _, _, _, err := modeDial(t, n, f, log, "192.0.2.20:445"); err != nil {
		t.Fatal(err)
	}
	// The router moved it again: the connect at 192.0.2.20 fails, and one
	// scan finds it at 192.0.2.21.
	n.mu.Lock()
	n.answers = map[netip.Addr][3]string{netip.MustParseAddr("192.0.2.21"): {"SERIAL-A", "S50_A", "Seestar S50"}}
	n.mu.Unlock()
	got, calls, scans, err := modeDial(t, n, f, log, "192.0.2.21:445")
	if err != nil || got != "session at 192.0.2.21:445" || !slices.Equal(calls, []string{"192.0.2.20:445", "192.0.2.21:445"}) || scans != 2 {
		t.Fatalf("after a failed connect = %q, %v; calls %q scans %d", got, err, calls, scans)
	}
	got, calls, scans, err = modeDial(t, n, f, log, "192.0.2.21:445")
	if err != nil || !slices.Equal(calls, []string{"192.0.2.21:445"}) || scans != 2 {
		t.Fatalf("after the rescan = %q, %v; calls %q scans %d", got, err, calls, scans)
	}
	// The same rules hold in the rescan: another serial at the saved
	// address blocks it, and nothing found keeps no address, so the next
	// connect scans again instead of dialing a blocked address.
	n.mu.Lock()
	n.answers = map[netip.Addr][3]string{netip.MustParseAddr("192.0.2.7"): {"SERIAL-C", "other", "Seestar S30"}}
	n.mu.Unlock()
	_, calls, scans, err = modeDial(t, n, f, log, "192.0.2.7:445")
	if !errors.Is(err, landiscovery.ErrOtherTelescope) || !slices.Equal(calls, []string{"192.0.2.21:445"}) || scans != 3 {
		t.Fatalf("other telescope in the rescan: %v; calls %q scans %d", err, calls, scans)
	}
	_, calls, scans, err = modeDial(t, n, f, log, "192.0.2.7:445")
	if !errors.Is(err, landiscovery.ErrOtherTelescope) || calls != nil || scans != 4 {
		t.Fatalf("the connect after: %v; calls %q scans %d", err, calls, scans)
	}
	if !strings.Contains(logs.String(), `other="other (Seestar S30, serial SERIAL-C)"`) {
		t.Errorf("logs: %s", logs.String())
	}
}

// connect scans before every connect.
func TestFollowDialConnectScansEachConnect(t *testing.T) {
	n := twoTelescopes()
	useLAN(t, n)
	f := newFollow(Saved{Endpoint: "192.0.2.20:445", LANSerial: "SERIAL-A", LANScan: "connect"}, none)
	for i := range 3 {
		_, calls, scans, err := modeDial(t, n, f, slog.New(slog.DiscardHandler), "192.0.2.20:445")
		if err != nil || !slices.Equal(calls, []string{"192.0.2.20:445"}) || scans != i+1 {
			t.Fatalf("connect %d: %v; calls %q scans %d", i, err, calls, scans)
		}
	}
}

// off dials the saved address, never scanning and logging
// nothing, even where a scan would find the telescope moved or another
// one at the saved address.
func TestFollowDialOffNeverScans(t *testing.T) {
	n := twoTelescopes()
	useLAN(t, n)
	var logs bytes.Buffer
	log := diagnostic.Logger("text", &logs, slog.LevelDebug)
	for _, saved := range []string{"192.0.2.7:445", "192.0.2.40:445"} {
		f := newFollow(Saved{Endpoint: saved, LANSerial: "SERIAL-A", LANScan: "off"}, none)
		for range 2 {
			got, calls, scans, err := modeDial(t, n, f, log, saved)
			if err != nil || got != fakeSession("session at "+saved) || !slices.Equal(calls, []string{saved}) || scans != 0 {
				t.Fatalf("off at %s = %q, %v; calls %q scans %d", saved, got, err, calls, scans)
			}
		}
		_, calls, scans, err := modeDial(t, n, f, log)
		if !errors.Is(err, syscall.ECONNREFUSED) || !slices.Equal(calls, []string{saved}) || scans != 0 {
			t.Fatalf("off, saved address down: %v; calls %q scans %d", err, calls, scans)
		}
	}
	if logs.Len() != 0 || n.listens != 0 {
		t.Errorf("off logged %q, opened %d sockets", logs.String(), n.listens)
	}
}

// --scan takes start, connect or off on the four commands, whatever else
// is given; anything else is a usage error.
func TestScanFlagUsage(t *testing.T) {
	t.Cleanup(readonly.Reset)
	for _, cmd := range []string{"sync", "find", "browse", "delete"} {
		for _, value := range []string{"never", "", "Start", "on"} {
			code, _, errs := invoke(t, cmd, "--endpoint", "127.0.0.1:4445", "--dest", t.TempDir(), "--scan="+value)
			if code != exitUsage || !strings.Contains(errs, "--scan") || !strings.Contains(errs, "use start, connect or off") {
				t.Errorf("%s --scan=%q: exit %d\n%s", cmd, value, code, errs)
			}
		}
		code, out, _ := invoke(t, cmd, "--help")
		if code != exitOK || !strings.Contains(out, "--scan string") || !strings.Contains(out, "start (the default) scans once per run") {
			t.Errorf("%s --help: exit %d\n%s", cmd, code, out)
		}
	}
}

// findScans runs find with --profile default and args on a network where
// the saved telescope moved, and reports the exit and the scans.
func findScans(t *testing.T, saved Saved, args ...string) (int, int, []string) {
	t.Helper()
	readonly.Reset()
	n := twoTelescopes()
	useLAN(t, n)
	useProfile(t, saved)
	dev := newDevice()
	dev.set("a.fit", "abc", findDir)
	dev.use(t)
	useDial(t)
	device := dial
	var calls []string
	dial = func(ctx context.Context, endpoint string, log *slog.Logger) (remote.Session, error) {
		calls = append(calls, endpoint)
		if endpoint != "192.0.2.20:445" && endpoint != "127.0.0.1:4445" {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
		}
		return device(ctx, endpoint, log)
	}
	code, _, _ := invoke(t, append([]string{"find", "--profile", "default"}, args...)...)
	return code, n.scans(), calls
}

// --scan overrides the profile's setting, either way, and a given
// --endpoint or a profile without a serial is dialed unscanned whatever
// it says.
func TestScanFlagOverridesProfile(t *testing.T) {
	t.Cleanup(readonly.Reset)
	moved := Saved{Endpoint: "192.0.2.7:445", LANSerial: "SERIAL-A"}
	for _, tc := range []struct {
		saved string
		args  []string
		scans int
		calls []string
	}{
		{"", nil, 1, []string{"192.0.2.20:445"}},
		{"off", nil, 0, []string{"192.0.2.7:445"}},
		{"off", []string{"--scan=start"}, 1, []string{"192.0.2.20:445"}},
		{"off", []string{"--scan=connect"}, 1, []string{"192.0.2.20:445"}},
		{"connect", []string{"--scan=off"}, 0, []string{"192.0.2.7:445"}},
		{"start", []string{"--scan", "off"}, 0, []string{"192.0.2.7:445"}},
		{"connect", []string{"--endpoint", "127.0.0.1:4445", "--scan=connect"}, 0, []string{"127.0.0.1:4445"}},
	} {
		s := moved
		s.LANScan = tc.saved
		code, scans, calls := findScans(t, s, tc.args...)
		wantCode := exitOK
		if tc.calls[0] == "192.0.2.7:445" {
			wantCode = exitFailure
		}
		if code != wantCode || scans != tc.scans || !slices.Equal(calls, tc.calls) {
			t.Errorf("saved %q args %q: exit %d scans %d calls %q; want exit %d scans %d calls %q", tc.saved, tc.args, code, scans, calls, wantCode, tc.scans, tc.calls)
		}
	}
	// A profile saved without a serial: nothing to follow.
	code, scans, calls := findScans(t, Saved{Endpoint: "192.0.2.20:445", LANScan: "connect"}, "--scan=connect")
	if code != exitOK || scans != 0 || !slices.Equal(calls, []string{"192.0.2.20:445"}) {
		t.Errorf("no serial: exit %d scans %d calls %q", code, scans, calls)
	}
}
