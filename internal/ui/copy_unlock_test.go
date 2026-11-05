package ui

import (
	"context"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/privatefile"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
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
		{Settings{Email: "synthetic@example.invalid"}, false},
		{Settings{Route: routeLAN, LANAddress: "192.0.2.10:445"}, false},
		{Settings{Email: "synthetic@example.invalid", RouteEstablished: true}, true},
		{Settings{Route: routeLAN, LANAddress: "192.0.2.10:445", RouteEstablished: true}, true},
	} {
		if got := routeSavedIn(tc.s); got != tc.want {
			t.Errorf("%+v = %v", tc.s, got)
		}
	}
}

// Only a Connect that reaches its save, or a LAN address that passed
// Test, records a route in config.json, which opens the copy tabs at the
// next launch. A password save, a retry or a failed write never does.
func TestRouteEstablishedSurvivesRelaunch(t *testing.T) {
	// relaunched reports whether the tabs open at the next launch.
	relaunched := func(e *routeEnv) bool {
		e.t.Helper()
		loaded, err := loadSettings(e.s.storage)
		return err == nil && routeSavedIn(loaded)
	}
	// savePassword saves a password with a draft that claims a route.
	savePassword := func(e *routeEnv) {
		e.t.Helper()
		s := testSettings()
		s.Remember, s.RouteEstablished = true, true
		body, _ := json.Marshal(input{Settings: s, Password: testPassword})
		if code, reply := e.post("/api/password/save", string(body)); code != 200 {
			e.t.Fatalf("password save = %d %s", code, reply)
		}
	}
	// connect starts a run that waits for clients, which saves its
	// settings once it is armed, and waits for the save.
	connect := func(e *routeEnv) {
		e.t.Helper()
		e.account()
		e.s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status) error {
			status.MarkArmed()
			<-ctx.Done()
			return nil
		}
		s := testSettings()
		s.Idle = "2m"
		if code, body := e.start(s); code != 200 {
			e.t.Fatalf("Connect = %d %s", code, body)
		}
		e.until("the save at the boundary", func() bool {
			e.s.controller.mu.Lock()
			defer e.s.controller.mu.Unlock()
			return e.s.controller.saveAttempted && (e.s.controller.pendingSave != nil || e.s.controller.lastSaved.Idle == "2m")
		})
	}
	fresh := func(t *testing.T) *routeEnv {
		e := newRouteEnv(t)
		e.s.settings, e.s.controller.lastSaved = defaultSettings(), defaultSettings()
		return e
	}
	t.Run("password only", func(t *testing.T) {
		e := fresh(t)
		savePassword(e)
		if e.copyStatus().Unlocked || relaunched(e) {
			t.Error("a password save opened the copy tabs")
		}
	})
	t.Run("connect", func(t *testing.T) {
		e := fresh(t)
		connect(e)
		if !e.copyStatus().Unlocked || !relaunched(e) {
			t.Error("a Connect didn't open the copy tabs")
		}
		// A later password save keeps it.
		e.account().stop()
		savePassword(e)
		if !relaunched(e) {
			t.Error("a password save dropped the route")
		}
	})
	t.Run("LAN save", func(t *testing.T) {
		e := fresh(t)
		e.lan("192.0.2.10")
		if !relaunched(e) {
			t.Error("a LAN save didn't open the copy tabs at relaunch")
		}
	})
	t.Run("failed persistence", func(t *testing.T) {
		e := fresh(t)
		// config.json can't replace a directory.
		config := filepath.Join(e.s.storage.root, "config.json")
		d, err := privatefile.Directory(config)
		if err != nil {
			t.Fatal(err)
		}
		d.Close()
		if code, _ := e.post("/api/route", `{"settings":{"route":"lan","lan_address":"192.0.2.10"}}`); code == 200 {
			t.Error("a failed LAN save succeeded")
		}
		connect(e)
		if e.copyStatus().Unlocked {
			t.Error("a failed save opened the copy tabs")
		}
		e.account().stop()
		if err := os.Remove(config); err != nil {
			t.Fatal(err)
		}
		// The retried save stores the settings, but not a route.
		if code, body := e.post("/api/retry-storage", `{}`); code != 200 {
			t.Fatalf("retry = %d %s", code, body)
		}
		if _, err := loadSettings(e.s.storage); err != nil {
			t.Fatalf("the retry saved nothing: %v", err)
		}
		if e.copyStatus().Unlocked || relaunched(e) {
			t.Error("a retry opened the copy tabs")
		}
	})
}

// Activity's Files names both settings files in the state dir.
func TestStatusNamesSettingsFiles(t *testing.T) {
	e := newRouteEnv(t)
	root, _ := filepath.Abs(e.s.storage.root)
	if got := e.copyStatus().Files; got.Config != filepath.Join(root, "config.json") || got.Copy != filepath.Join(root, "copy.json") {
		t.Errorf("files = %+v", got)
	}
}
