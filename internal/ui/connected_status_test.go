package ui

import (
	"encoding/json/v2"
	"strconv"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

func TestConnectedStatusAndEvents(t *testing.T) {
	const label = "Seestar S50 Pro synthetic-selected-1234"
	for _, tc := range []struct{ state, want string }{
		{"Ready", "Connected to " + label},
		{"Waiting for a local client", "Waiting for an app to connect to " + label},
		{"Busy — waiting for the other session", label + " is occupied. Retrying automatically."},
		{"Seestar not reachable", label + " isn't reachable. Check this computer's internet connection and make sure it is awake and connected to Wi-Fi. Retrying automatically."},
		{"Reconnecting", "Connection to " + label + " interrupted. Retrying automatically."},
		{"Releasing idle session", "Releasing idle session with " + label + "…"},
		{"Reconnecting — waiting for cleanup/retry", "Waiting before retrying " + label + "…"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			got := telescopeText(tc.state, label)
			if got != tc.want {
				t.Fatalf("status=%q want=%q", got, tc.want)
			}
			if telescopeText(tc.state, "") != uiText(tc.state) {
				t.Fatal("unknown telescope changed existing status")
			}
			if eventCode(got, label) != "lifecycle" {
				t.Fatal("selected lifecycle event rejected")
			}
			for _, text := range []string{got + " arbitrary", "arbitrary " + got, "Connected to arbitrary", "private error"} {
				if eventCode(text, label) != "" {
					t.Fatalf("arbitrary text accepted: %q", text)
				}
			}
			if eventCode(got, "") != "" {
				t.Fatal("dynamic event accepted without selection context")
			}
			c := testController(nil)
			c.log = &eventLog{}
			c.status = proxy.NewStatus()
			c.status.SetSelectedDevice(cloudhttp.Device{Model: "Seestar S50 Pro", Serial: "synthetic-selected-1234"})
			c.recordProgress(got)
			events, _, _ := c.log.snapshot()
			if len(events) != 1 || events[0].Message != tc.want || events[0].Code != "lifecycle" {
				t.Fatalf("events=%+v", events)
			}
			response := map[string]any{"active": true, "status": got, "selected_device": label, "settings": testSettings(), "version": "synthetic", "effective": map[string]any{"listen": "this_computer", "idle": map[string]string{"mode": "keep_connected"}, "ports": map[string]int{}}, "password": map[string]bool{"memory": true}, "events": events}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			runPageRegression(t, "connected-status-test.js", "checkConnectedStatus("+string(encoded)+","+strconv.Quote(tc.want)+")")
		})
	}
	for _, text := range []string{"Connecting", "Stopped", "unrecognized"} {
		if telescopeText(text, label) != uiText(text) {
			t.Fatal("nonrunning status changed")
		}
	}
}
