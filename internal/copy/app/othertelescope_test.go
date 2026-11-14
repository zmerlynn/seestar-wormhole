package app

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
)

// The owner's setup of 2026-11-14: the telescope is saved as SERIAL-A at
// 192.0.2.85:4445, but only TEST0002 answered the scan there.
var (
	otherErr = fmt.Errorf("find telescope: %w", &landiscovery.OtherTelescopeError{
		Serial: "SERIAL-A", Address: netip.MustParseAddr("192.0.2.85"),
		Other: &landiscovery.Telescope{Serial: "TEST0002", Name: "Relay", Model: "S50 Pro"},
	})
	otherTarget = Target{Endpoint: "192.0.2.85:4445", Serial: "SERIAL-A"}
)

// otherName names a target's telescope as the app's Mount does once it
// knows the saved telescope's model.
func otherName(t Target) string {
	return cloudhttp.Device{Serial: t.Serial, Model: "S50 Pro"}.DisplayName()
}

const (
	otherTitle = "Another telescope answered at 192.0.2.85."
	otherWhat  = "Expected S50 Pro SERIAL-A, but S50 Pro TEST0002 answered there."
)

// Copying, on and once, says another telescope answered rather than that
// nothing did, and names both, without the Local Network hint.
func TestCopyOtherTelescope(t *testing.T) {
	setHostOS(t, "darwin")
	e := newCopyEnv(t, fstest.MapFS{"MyWorks/a.jpg": image("abc")})
	e.c.name = otherName
	e.dialErr = otherErr
	if err := e.c.start(e.set, otherTarget); err != nil {
		t.Fatal(err)
	}
	v := e.wait(stUnreachable)
	if v.Title != otherTitle || !v.On || v.Contact != "err" || !strings.HasPrefix(v.Text, "Trying again at ") ||
		!strings.HasSuffix(v.Text, ". "+otherWhat+" Copying stays on and keeps trying. To switch telescopes, stop copying, then pick the telescope in Connection and Save.") {
		t.Errorf("on: %+v", v)
	}
	e.c.stop()
	e.waitOff()

	e.set.Once = true
	if err := e.c.start(e.set, otherTarget); err != nil {
		t.Fatal(err)
	}
	e.waitOff()
	v = e.wait(stDone)
	if v.Title != otherTitle+" Checked once; copying is off." || v.Text != otherWhat+" Pick the telescope in Connection and Save." {
		t.Errorf("once: %+v", v)
	}
}

// Preview, and Free up space's check, say another telescope answered.
func TestPreviewOtherTelescope(t *testing.T) {
	setHostOS(t, "darwin")
	e := newPreviewEnv(t, fstest.MapFS{"MyWorks/a.jpg": image("a")})
	e.p.name = otherName
	e.dialErr = otherErr
	v := e.runAt(otherTarget)
	if v.State != pvFailed || v.Title != otherTitle || v.Text != otherWhat+" Pick the telescope in Connection and Save, or Preview again to retry." {
		t.Errorf("preview: %+v", v)
	}

	st := freeStatus{state: frCheckFail, target: otherTarget, err: otherErr}
	if v := st.view(otherName); v.Title != otherTitle || v.Text != otherWhat+" Pick the telescope in Connection and Save, or Preview again to retry." {
		t.Errorf("free up space: %+v", v)
	}
}

// When others filled the scan and no address was left to name, the text
// says the scan couldn't choose one, not that the saved telescope didn't
// answer, as it may have; a page with no names says Seestar.
func TestOtherTelescopesUnnamed(t *testing.T) {
	err := &landiscovery.OtherTelescopeError{Serial: "SERIAL-A"}
	title, what, ok := otherAnswered(err, otherName)
	if !ok || title != "Too many addresses answered the scan." || what != "The scan couldn't safely choose an address for S50 Pro SERIAL-A." {
		t.Errorf("got %q %q %v", title, what, ok)
	}
	if _, what, _ := otherAnswered(otherErr, nil); what != "Expected Seestar, but S50 Pro TEST0002 answered there." {
		t.Errorf("no names: %q", what)
	}
	if _, _, ok := otherAnswered(noRoute, otherName); ok {
		t.Error("a connect nothing answered is another telescope")
	}
}

// Logs and Activity name both telescopes.
func TestReasonOtherTelescope(t *testing.T) {
	if got, want := reason(withHint(otherErr)), "another telescope, Relay (S50 Pro, serial TEST0002), answered at 192.0.2.85 instead of serial SERIAL-A"; got != want {
		t.Errorf("reason = %q, want %q", got, want)
	}
}

// Whatever text the other telescope's reply carried, the text names it
// within the message budget: by its model, capped, and a valid serial, or
// as another telescope. Logs and Activity bound it the same way.
func TestOtherTelescopeBounded(t *testing.T) {
	del := strings.Repeat("\x7f", 3700)
	capped := strings.Repeat(`\x7f`, 15) + "..."
	for _, c := range []struct {
		other      landiscovery.Telescope
		what, logs string
	}{
		{landiscovery.Telescope{Serial: "TEST0002", Name: del, Model: del},
			"Expected S50 Pro SERIAL-A, but " + capped + " TEST0002 answered there.",
			"another telescope, " + capped + " (" + capped + ", serial TEST0002), answered at 192.0.2.85 instead of serial SERIAL-A"},
		{landiscovery.Telescope{Serial: del, Name: del, Model: del},
			"Expected S50 Pro SERIAL-A, but another telescope answered there.",
			"another telescope, serial " + capped + ", answered at 192.0.2.85 instead of serial SERIAL-A"},
	} {
		err := fmt.Errorf("find telescope: %w", &landiscovery.OtherTelescopeError{Serial: "SERIAL-A", Address: netip.MustParseAddr("192.0.2.85"), Other: &c.other})
		if title, what, ok := otherAnswered(err, otherName); !ok || title != otherTitle || what != c.what {
			t.Errorf("got %q %q %v, want %q", title, what, ok, c.what)
		}
		if got := reason(err); got != c.logs || len(got) > 4096 {
			t.Errorf("reason = %q, want %q", got, c.logs)
		}
	}
}
