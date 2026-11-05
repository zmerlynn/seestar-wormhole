package ui

import (
	"encoding/json/v2"
	"path/filepath"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
)

// copyStatus is the part of /api/status that opens the copy tabs.
type copyStatus struct {
	Unlocked bool          `json:"copy_unlocked"`
	Reach    string        `json:"copy_reach"`
	Files    settingsFiles `json:"files"`
}

func (e *routeEnv) copyStatus() copyStatus {
	e.t.Helper()
	code, body := e.get("/api/status")
	var v copyStatus
	if err := json.Unmarshal([]byte(body), &v); code != 200 || err != nil {
		e.t.Fatalf("status = %d %s, %v", code, body, err)
	}
	return v
}

// Copy, Browse and Free up space open once a route is saved: an address
// on this network that passed Test, and stay open.
func TestCopyUnlocksOnSavedLAN(t *testing.T) {
	e := newRouteEnv(t)
	if got := e.copyStatus(); got.Unlocked || got.Reach != "not_connected" {
		t.Errorf("before a route = %+v", got)
	}
	if code, _ := e.post("/api/route", `{"settings":{"route":"lan","lan_address":"192.0.2.99"}}`); code != 400 {
		t.Errorf("an unreachable address saved: %d", code)
	}
	if got := e.copyStatus(); got.Unlocked {
		t.Errorf("a failed Test opened Copy: %+v", got)
	}
	e.lan("192.0.2.10")
	if got := e.copyStatus(); !got.Unlocked || got.Reach != "" {
		t.Errorf("after a LAN save = %+v", got)
	}
	// Back to the account: open, and waiting for the connection.
	if code, body := e.post("/api/route", `{"settings":{"route":"account"}}`); code != 200 {
		t.Fatalf("account route = %d %s", code, body)
	}
	if got := e.copyStatus(); !got.Unlocked || got.Reach != "not_connected" {
		t.Errorf("after switching back = %+v", got)
	}
}

// Through the account, the tabs open once the connection is ready, and
// say when SMB is off.
func TestCopyUnlocksOnConnection(t *testing.T) {
	e := newRouteEnv(t)
	a := e.account()
	settings := testSettings()
	settings.Ports = []proxyrun.Port{{Local: 0, Device: 445}}
	if code, body := e.start(settings); code != 200 {
		t.Fatalf("Connect = %d %s", code, body)
	}
	if got := e.copyStatus(); got.Unlocked || got.Reach != "not_connected" {
		t.Errorf("while connecting = %+v", got)
	}
	a.markReady("SYNTHETIC1")
	if got := e.copyStatus(); !got.Unlocked || got.Reach != "smb_off" {
		t.Errorf("connected with SMB off = %+v", got)
	}
	a.stop()
	if got := e.copyStatus(); !got.Unlocked || got.Reach != "not_connected" {
		t.Errorf("after Stop = %+v", got)
	}
}

func TestRouteSavedIn(t *testing.T) {
	for _, tc := range []struct {
		s    Settings
		want bool
	}{
		{Settings{}, false},
		{Settings{Email: "synthetic@example.invalid"}, true},
		{Settings{Route: routeAccount, Email: "synthetic@example.invalid"}, true},
		{Settings{Route: routeLAN}, false},
		{Settings{Route: routeLAN, LANAddress: "192.0.2.10:445"}, true},
	} {
		if got := routeSavedIn(tc.s); got != tc.want {
			t.Errorf("%+v = %v", tc.s, got)
		}
	}
}

// Activity's Files names both settings files in the state dir.
func TestStatusNamesSettingsFiles(t *testing.T) {
	e := newRouteEnv(t)
	root, _ := filepath.Abs(e.s.storage.root)
	if got := e.copyStatus().Files; got.Config != filepath.Join(root, "config.json") || got.Copy != filepath.Join(root, "copy.json") {
		t.Errorf("files = %+v", got)
	}
}
