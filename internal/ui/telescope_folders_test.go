package ui

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
)

// The telescope saved keys its folder by its serial on either route, or
// by the address saved without one.
func TestSavedTelescope(t *testing.T) {
	for _, c := range []struct {
		name string
		s    Settings
		want string
	}{
		{"lan serial", Settings{Route: routeLAN, LANAddress: "192.0.2.10", LANSerial: "SYNTH0001"}, "serial:SYNTH0001"},
		{"lan address", Settings{Route: routeLAN, LANAddress: "192.0.2.10"}, "address:192.0.2.10:445"},
		{"lan nothing", Settings{Route: routeLAN}, ""},
		{"account serial", Settings{Route: routeAccount, Serial: "SYNTH0001", LANAddress: "192.0.2.10"}, "serial:SYNTH0001"},
		{"account none", Settings{Route: routeAccount}, ""},
	} {
		if got := SavedTelescope(c.s); got != c.want {
			t.Errorf("%s: %q; want %q", c.name, got, c.want)
		}
	}
}

// The name a folder is suggested under: the connected telescope's label,
// the model heard with a serial, or the model heard at an address.
func TestTelescopeName(t *testing.T) {
	s := testServer(t)
	at := netip.MustParseAddr("192.0.2.10")
	s.lan.remember(1, at, landiscovery.ScanResult{Telescopes: []landiscovery.Telescope{{Serial: "SYNTH0001", Model: "Seestar S30", Addresses: []netip.Addr{at}}}})
	for _, c := range []struct {
		t    copyapp.Target
		want string
	}{
		{copyapp.Target{Device: "Seestar S50 SYNTH0002"}, "Seestar S50 SYNTH0002"},
		{copyapp.Target{Serial: "SYNTH0001", Endpoint: "192.0.2.10:445"}, "Seestar S30 SYNTH0001"},
		{copyapp.Target{Serial: "SYNTH0003", Endpoint: "192.0.2.12:445"}, "Seestar SYNTH0003"},
		{copyapp.Target{Endpoint: "192.0.2.10:445"}, "Seestar S30 192.0.2.10 445"},
		{copyapp.Target{Endpoint: "192.0.2.11:445"}, "Seestar 192.0.2.11 445"},
	} {
		if got := s.telescopeName(c.t); got != c.want {
			t.Errorf("%+v: %q; want %q", c.t, got, c.want)
		}
	}
}

// A copy.json with telescopes loads them, and a save keeps an entry's
// members this version doesn't know; one without them loads as before.
func TestCopySettingsTelescopes(t *testing.T) {
	st := testStorage(t)
	body := `{"version":1,"folder":"/synthetic/Seestar","telescopes":{"serial:SYNTH0001":{"folder":"/synthetic/Seestar","color":"red"},"address:192.0.2.10:445":{"folder":"/synthetic/Other"}}}`
	if err := st.writeBytes(copyName, []byte(body)); err != nil {
		t.Fatal(err)
	}
	c, set, _, err := loadCopySettings(st)
	if err != nil || set == nil {
		t.Fatalf("load = %+v, %v", set, err)
	}
	if set.Folder != "/synthetic/Seestar" || set.FolderFor("serial:SYNTH0001") != "/synthetic/Seestar" ||
		set.FolderFor("address:192.0.2.10:445") != "/synthetic/Other" || set.FolderFor("serial:SYNTH0002") != "" {
		t.Errorf("loaded %+v", set)
	}
	set.IntervalMinutes = 30
	if err := c.save(*set); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(st.root, copyName))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Folder     string                    `json:"folder"`
		Telescopes map[string]jsontext.Value `json:"telescopes"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.Folder != "/synthetic/Seestar" || !bytes.Contains(f.Telescopes["serial:SYNTH0001"], []byte(`"color":"red"`)) || len(f.Telescopes) != 2 {
		t.Errorf("copy.json = %s", b)
	}

	// An older file has no telescopes, and every telescope's folder is
	// its folder until one is claimed.
	if err := st.writeBytes(copyName, []byte(`{"version":1,"folder":"/synthetic/Seestar"}`)); err != nil {
		t.Fatal(err)
	}
	if _, set, _, err := loadCopySettings(st); err != nil || set == nil || set.Telescopes != nil || set.FolderFor("serial:SYNTH0001") != "/synthetic/Seestar" {
		t.Errorf("older file = %+v, %v", set, err)
	}
}

// The telescope saved before telescopes keeps the folder on each route:
// the first copy gives its key the folder, and copies into it.
func TestRouteUpgradeKeepsTheFolder(t *testing.T) {
	for _, c := range []struct {
		name  string
		key   string
		setup func(e *routeEnv)
	}{
		{"lan serial", "serial:SYNTH0001", func(e *routeEnv) {
			e.lan("192.0.2.10")
			e.s.mu.Lock()
			e.s.settings.LANSerial = "SYNTH0001"
			e.s.mu.Unlock()
		}},
		{"lan address", "address:192.0.2.10:445", func(e *routeEnv) { e.lan("192.0.2.10") }},
		{"account", "serial:SYNTH0002", func(e *routeEnv) {
			a := e.account()
			if code, body := e.start(testSettings()); code != 200 {
				t.Fatalf("Connect = %d %s", code, body)
			}
			a.markReady("SYNTH0002")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			folder := filepath.Join(t.TempDir(), "Seestar")
			old := copyapp.DefaultSettings()
			old.Folder = folder
			var mu sync.Mutex
			var saves []copyapp.Settings
			e := newRouteEnv(t, func(o *copyapp.MountOptions) {
				o.Settings = &old
				o.Save = func(s copyapp.Settings) error {
					mu.Lock()
					defer mu.Unlock()
					saves = append(saves, s)
					return nil
				}
			})
			c.setup(e)
			mu.Lock()
			n := len(saves)
			mu.Unlock()
			if n != 0 {
				t.Fatalf("connecting saved copy's settings %d times", n)
			}
			if code, body := e.post("/copy/api/copy", `{"action":"start"}`); code != 200 {
				t.Fatalf("start copying = %d %s", code, body)
			}
			e.until("the copy", func() bool {
				entries, _ := os.ReadDir(filepath.Join(folder, "MyWorks"))
				return len(entries) > 0
			})
			mu.Lock()
			defer mu.Unlock()
			if len(saves) == 0 || saves[0].Folder != folder || !maps.Equal(folderMapOf(saves[0]), map[string]string{c.key: folder}) {
				t.Errorf("saves = %+v", saves)
			}
		})
	}
}

func folderMapOf(s copyapp.Settings) map[string]string {
	m := map[string]string{}
	for k, t := range s.Telescopes {
		m[k] = t.Folder
	}
	return m
}

// No line of gui.log, event, failure or Activity names a telescope's serial, its
// address or its folder, through an address's folder, its adoption by
// the serial heard there, and a second telescope's folder.
func TestTelescopeFolderPrivacy(t *testing.T) {
	e := newTrailEnv(t)
	e.ready() // 192.0.2.10, keyed by its address, with e.folder
	e.pass(1)
	e.stopCopying()

	// A scan hears SERIAL-SENTINEL-A at the address: Copy offers the
	// address's folder, and nothing moves until it is accepted.
	e.s.mu.Lock()
	e.s.settings.LANSerial = "SERIAL-SENTINEL-A"
	e.s.mu.Unlock()
	_, body := e.get("/copy/api/status")
	if !strings.Contains(body, `"offer":{`) {
		t.Fatalf("no offer: %s", body)
	}
	e.mustPost("/copy/api/adopt", `{}`, 200)
	e.pass(2)
	e.stopCopying()

	// A second telescope may not share the folder, and copies into its own.
	e.lan("192.0.2.11")
	e.s.mu.Lock()
	e.s.settings.LANSerial = "SERIAL-SENTINEL-B"
	e.s.mu.Unlock()
	e.mustPost("/copy/api/copy", `{"action":"start"}`, 409)
	quoted, _ := json.Marshal(e.folder)
	e.mustPost("/copy/api/settings", `{"folder":`+string(quoted)+`,"interval":"5","files_at_once":"4","excludes":[],"includes":[],"skip_built_in":true,"start_copying":false}`, 400)
	e.settingsIn(e.folder + "-b")
	e.pass(3)
	e.stopCopying()

	data, err := os.ReadFile(filepath.Join(e.s.storage.root, "gui.log"))
	if err != nil {
		t.Fatal(err)
	}
	_, reply := e.get("/api/status")
	var status struct {
		Events   jsontext.Value `json:"events"`
		Failures jsontext.Value `json:"failures"`
	}
	if err := json.Unmarshal([]byte(reply), &status); err != nil {
		t.Fatal(err)
	}
	_, copyReply := e.get("/copy/api/status")
	var copyStatus struct {
		Activity jsontext.Value `json:"activity"`
	}
	if err := json.Unmarshal([]byte(copyReply), &copyStatus); err != nil || len(copyStatus.Activity) < 10 {
		t.Fatalf("copy status %s: %v", copyReply, err)
	}
	escaped, _ := json.Marshal(e.folder)
	for where, text := range map[string]string{"gui.log": string(data), "events": string(status.Events), "failures": string(status.Failures), "activity": string(copyStatus.Activity)} {
		// The fixture's files are named for their share's host.
		text = strings.NewReplacer("at_192.0.2.10.jpg", "", "at_192.0.2.11.jpg", "").Replace(text)
		for _, s := range []string{"SERIAL-SENTINEL", "192.0.2.10", "192.0.2.11", filepath.ToSlash(e.folder), string(escaped[1 : len(escaped)-1])} {
			if strings.Contains(text, s) {
				t.Errorf("%s holds %q", where, s)
			}
		}
	}
}

// heardEnv is a route env whose copy settings save to saves and fail with
// saveErr, with copy's KnowSerial wrapped by know, saved at an address
// on this network by itself with a folder.
type heardEnv struct {
	*routeEnv
	mu      sync.Mutex
	saves   []copyapp.Settings
	saveErr error
	knowErr error
}

func newHeardEnv(t *testing.T) *heardEnv {
	t.Helper()
	h := &heardEnv{}
	h.routeEnv = newRouteEnv(t, func(o *copyapp.MountOptions) {
		o.Save = func(s copyapp.Settings) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.saveErr != nil {
				return h.saveErr
			}
			h.saves = append(h.saves, s)
			return nil
		}
		o.Name = func(tg copyapp.Target) string { return h.s.telescopeName(tg) }
		o.KnowSerial = func(tg copyapp.Target) error {
			h.mu.Lock()
			err := h.knowErr
			h.mu.Unlock()
			if err != nil {
				return err
			}
			return h.s.knowSerial(tg)
		}
	})
	h.lan("192.0.2.10")
	h.settings()
	return h
}

// hear delivers a scan's answer from serial at 192.0.2.10, as a scan or
// Test hears it.
func (h *heardEnv) hear(serial string) {
	ip := netip.MustParseAddr("192.0.2.10")
	h.s.lan.remember(h.s.lan.begin(), ip, landiscovery.ScanResult{Telescopes: []landiscovery.Telescope{{Serial: serial, Model: "Seestar S30", Addresses: []netip.Addr{ip}}}})
}

type heardOffer struct {
	Name   string `json:"name"`
	Folder string `json:"folder"`
	Token  string `json:"token"`
}

// offer is the offer copy's status shows, or nil.
func (h *heardEnv) offer() *heardOffer {
	h.t.Helper()
	code, body := h.get("/copy/api/status")
	var v struct {
		Telescope struct {
			Offer *heardOffer `json:"offer"`
		} `json:"telescope"`
	}
	if err := json.Unmarshal([]byte(body), &v); code != 200 || err != nil {
		h.t.Fatalf("status = %d %s, %v", code, body, err)
	}
	return v.Telescope.Offer
}

func (h *heardEnv) savedSerial() string {
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	return h.s.settings.LANSerial
}

func (h *heardEnv) saveCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.saves)
}

// A scan or Test that hears a serial at an address saved by itself, with
// its own folder, offers that folder to the serial before any Connection
// Save, and nothing changes until it is accepted; accepting saves the
// serial with the address and gives its key the folder.
func TestHeardSerialOffersFolder(t *testing.T) {
	h := newHeardEnv(t)
	if o := h.offer(); o != nil {
		t.Fatalf("an offer before a serial was heard: %+v", o)
	}
	saves := h.saveCount()
	h.hear("SYNTH-HEARD-1")
	o := h.offer()
	if o == nil {
		t.Fatal("hearing a serial at the saved address did not offer its folder")
	}
	if o.Folder != h.folder || !strings.Contains(o.Name, "SYNTH-HEARD-1") || o.Token == "" {
		t.Errorf("offer = %+v", o)
	}
	if h.savedSerial() != "" || h.saveCount() != saves {
		t.Fatal("the offer changed which telescope has which folder before it was accepted")
	}
	// The offer shown was for another serial than the one heard now.
	h.hear("SYNTH-HEARD-2")
	if code, body := h.post("/copy/api/adopt", `{"token":"`+o.Token+`"}`); code != 409 {
		t.Errorf("a stale offer accepted = %d %s", code, body)
	}
	if h.savedSerial() != "" || h.saveCount() != saves {
		t.Fatal("a stale acceptance changed which telescope has which folder")
	}
	h.hear("SYNTH-HEARD-1")
	if code, body := h.post("/copy/api/adopt", `{}`); code != 200 {
		t.Fatalf("adopt = %d %s", code, body)
	}
	if got := h.savedSerial(); got != "SYNTH-HEARD-1" {
		t.Errorf("saved serial = %q", got)
	}
	var config Settings
	if err := h.s.storage.read("config.json", &config); err != nil || config.LANSerial != "SYNTH-HEARD-1" || config.LANAddress != "192.0.2.10:445" {
		t.Errorf("config.json = %+v, %v", config, err)
	}
	h.mu.Lock()
	last := h.saves[len(h.saves)-1]
	h.mu.Unlock()
	if !maps.Equal(folderMapOf(last), map[string]string{"serial:SYNTH-HEARD-1": h.folder}) {
		t.Errorf("telescopes = %v", folderMapOf(last))
	}
	if o := h.offer(); o != nil {
		t.Errorf("the offer stayed after it was accepted: %+v", o)
	}
	target, err := h.s.copyTarget()
	if err != nil || target.Telescope != "serial:SYNTH-HEARD-1" || target.Heard != "" {
		t.Errorf("target = %+v, %v", target, err)
	}
}

// An acceptance while copying is on is refused, and changes nothing: the
// copier's telescope would move under it. The pass is held in its dial,
// so copying stays on for as long as the test needs.
func TestHeardSerialAcceptanceRefusedWhileCopying(t *testing.T) {
	h := newHeardEnv(t)
	saves := h.saveCount()
	h.hear("SYNTH-HEARD-1")
	h.set(&h.holdDial, make(chan struct{}))
	if code, body := h.post("/copy/api/copy", `{"action":"start"}`); code != 200 {
		t.Fatalf("start copying = %d %s", code, body)
	}
	if code, body := h.post("/copy/api/adopt", `{}`); code != 409 || !strings.Contains(body, "Stop copying") {
		t.Errorf("adopt while copying = %d %s", code, body)
	}
	if h.savedSerial() != "" || h.saveCount() != saves || h.offer() == nil {
		t.Error("an acceptance while copying changed something")
	}
	if code, body := h.post("/copy/api/copy", `{"action":"stop"}`); code != 200 {
		t.Fatalf("stop copying = %d %s", code, body)
	}
}

// An acceptance whose serial can't be saved changes nothing; one whose
// serial is saved but whose folder isn't leaves the folder at the address,
// offered again to the telescope saved with its serial.
func TestHeardSerialAcceptanceFails(t *testing.T) {
	h := newHeardEnv(t)
	saves := h.saveCount()
	h.hear("SYNTH-HEARD-1")
	h.mu.Lock()
	h.knowErr = errors.New("synthetic")
	h.mu.Unlock()
	if code, body := h.post("/copy/api/adopt", `{}`); code != 409 || !strings.Contains(body, "Couldn't save settings") {
		t.Errorf("adopt with no serial saved = %d %s", code, body)
	}
	if h.savedSerial() != "" || h.saveCount() != saves || h.offer() == nil {
		t.Fatal("a failed acceptance changed something")
	}
	h.mu.Lock()
	h.knowErr, h.saveErr = nil, errors.New("synthetic")
	h.mu.Unlock()
	if code, body := h.post("/copy/api/adopt", `{}`); code != 409 || !strings.Contains(body, "Couldn't save settings") {
		t.Errorf("adopt with no folder saved = %d %s", code, body)
	}
	if h.savedSerial() != "SYNTH-HEARD-1" {
		t.Fatal("the serial wasn't saved")
	}
	o := h.offer()
	if o == nil || o.Folder != h.folder {
		t.Fatalf("the address's folder isn't offered again: %+v", o)
	}
	h.mu.Lock()
	h.saveErr = nil
	h.mu.Unlock()
	if code, body := h.post("/copy/api/adopt", `{}`); code != 200 {
		t.Fatalf("adopt again = %d %s", code, body)
	}
	h.mu.Lock()
	last := h.saves[len(h.saves)-1]
	h.mu.Unlock()
	if !maps.Equal(folderMapOf(last), map[string]string{"serial:SYNTH-HEARD-1": h.folder}) {
		t.Errorf("telescopes = %v", folderMapOf(last))
	}
}
