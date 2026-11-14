package ui

import (
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
)

// otherTelescopeServer is the owner's setup of 2026-11-14: the S50 Pro is
// saved with SERIAL-A at 192.0.2.85:4445, where a relay's SMB port
// answers, but the scan hears only TEST0002 there.
func otherTelescopeServer(t *testing.T, once bool) (*server, *lanTCP) {
	t.Helper()
	l := &lanTCP{up: []string{"192.0.2.85:445", "192.0.2.85:4445"}}
	useLANTCP(t, l)
	useLAN(t, &lanNet{answers: map[netip.Addr][3]string{netip.MustParseAddr("192.0.2.85"): {"TEST0002", "Relay", "S50 Pro"}}})
	s := testServer(t)
	s.controller.log = &eventLog{storage: s.storage}
	if err := s.controller.persistRoute(routeLAN, "192.0.2.85:4445", "SERIAL-A"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.settings.Serial, s.settings.Model = "SERIAL-A", "S50 Pro"
	s.mu.Unlock()
	set := copyapp.DefaultSettings()
	set.Folder, set.Once = filepath.Join(t.TempDir(), "copy"), once
	if err := os.Mkdir(set.Folder, 0o700); err != nil {
		t.Fatal(err)
	}
	s.copy = copyapp.NewMount(t.Context(), copyMountOptions(s, copyapp.MountOptions{Addr: netip.MustParseAddrPort(s.host), StateDir: t.TempDir(),
		Settings: &set, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	t.Cleanup(s.copy.Quit)
	return s, l
}

// The connect fails because another telescope answered at the saved
// address, not because nothing did.
func TestOtherTelescopeAnsweredCause(t *testing.T) {
	s, l := otherTelescopeServer(t, true)
	target, err := s.copyTarget()
	if err != nil {
		t.Fatal(err)
	}
	_, err = copyMountOptions(s, copyapp.MountOptions{}).Dial(t.Context(), target)
	if !errors.Is(err, landiscovery.ErrOtherTelescope) || l.take() != nil {
		t.Fatalf("dial: %v", err)
	}
}

const (
	otherTitle = "Another telescope answered at 192.0.2.85."
	otherWhat  = "Expected S50 Pro SERIAL-A, but S50 Pro TEST0002 answered there."
	otherRetry = otherWhat + " Pick the telescope in Connection and Save, or Preview again to retry."
	// otherLog is how Activity and the log name both telescopes.
	otherLog = "another telescope, Relay (S50 Pro, serial TEST0002), answered at 192.0.2.85 instead of serial SERIAL-A"
)

// copyPart waits until Copy's status at part (copy, copy.preview or
// free) is in one of states, and returns its state, title and text.
func copyPart(t *testing.T, s *server, part string, states ...string) (string, string, string) {
	t.Helper()
	type view struct {
		State string `json:"state"`
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		w := serve(s, request(s, "GET", "/copy/api/status", ""))
		var v struct {
			Copy struct {
				view
				Preview *view `json:"preview"`
			} `json:"copy"`
			Free view `json:"free"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		got := map[string]*view{"copy": &v.Copy.view, "copy.preview": v.Copy.Preview, "free": &v.Free}[part]
		if got != nil && slices.Contains(states, got.State) {
			return got.State, got.Title, got.Text
		}
	}
	w := serve(s, request(s, "GET", "/copy/api/status", ""))
	t.Fatalf("%s isn't %q: %s", part, states, w.Body)
	return "", "", ""
}

// namesBoth fails t unless the app's log names both serials, and, for
// copying, which logs its passes, Copy's Activity names both telescopes.
func namesBoth(t *testing.T, s *server, copying bool) {
	t.Helper()
	if w := serve(s, request(s, "GET", "/copy/api/status", "")); copying && !strings.Contains(w.Body.String(), otherLog) {
		t.Errorf("Copy's Activity doesn't name both: %s", w.Body)
	}
	if trail := trailText(t, s); !strings.Contains(trail, "TEST0002") || !strings.Contains(trail, "SERIAL-A") {
		t.Errorf("the log doesn't name both: %s", trail)
	}
}

// Copy's Preview names both telescopes, and so does the log.
func TestOtherTelescopePreview(t *testing.T) {
	s, _ := otherTelescopeServer(t, true)
	if w := doPost(s, "/copy/api/copy", `{"action":"preview"}`); w.Code != 200 {
		t.Fatalf("preview = %d %s", w.Code, w.Body)
	}
	if _, title, text := copyPart(t, s, "copy.preview", "failed"); title != otherTitle || text != otherRetry {
		t.Errorf("preview: %q %q", title, text)
	}
	namesBoth(t, s, false)
}

// Copying once, and copying on, say another telescope answered, and
// Activity names both.
func TestOtherTelescopeCopy(t *testing.T) {
	s, _ := otherTelescopeServer(t, true)
	if w := doPost(s, "/copy/api/copy", `{"action":"start"}`); w.Code != 200 {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	if _, title, text := copyPart(t, s, "copy", "done"); title != otherTitle+" Checked once; copying is off." || text != otherWhat+" Pick the telescope in Connection and Save." {
		t.Errorf("once: %q %q", title, text)
	}
	namesBoth(t, s, true)

	s, _ = otherTelescopeServer(t, false)
	if w := doPost(s, "/copy/api/copy", `{"action":"start"}`); w.Code != 200 {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	if _, title, text := copyPart(t, s, "copy", "unreachable"); title != otherTitle ||
		!strings.HasSuffix(text, ". "+otherWhat+" Pick the telescope in Connection and Save. Copying stays on and keeps trying.") {
		t.Errorf("on: %q %q", title, text)
	}
}

// Free up space's check says another telescope answered.
func TestOtherTelescopeFreeSpace(t *testing.T) {
	s, _ := otherTelescopeServer(t, true)
	if w := doPost(s, "/copy/api/delete", `{"action":"find","days":"0","trust":false}`); w.Code != 200 {
		t.Fatalf("find = %d %s", w.Code, w.Body)
	}
	if _, title, text := copyPart(t, s, "free", "checkfail"); title != otherTitle || text != otherRetry {
		t.Errorf("free up space: %q %q", title, text)
	}
}

// Browse's message names both telescopes.
func TestOtherTelescopeBrowse(t *testing.T) {
	s, _ := otherTelescopeServer(t, true)
	if code := browseGet(s, "/browse/", "same-origin"); code != 200 {
		t.Fatalf("opening Browse = %d", code)
	}
	r := request(s, "GET", "/browse/api/list", "")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w := serve(s, r)
	var v struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, w.Body)
	}
	if !strings.Contains(v.Message, otherLog) {
		t.Errorf("Browse says %d %s", w.Code, w.Body)
	}
}
