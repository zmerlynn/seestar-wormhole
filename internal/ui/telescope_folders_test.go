package ui

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
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
	c, set, err := loadCopySettings(st)
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
	if _, set, err := loadCopySettings(st); err != nil || set == nil || set.Telescopes != nil || set.FolderFor("serial:SYNTH0001") != "/synthetic/Seestar" {
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
