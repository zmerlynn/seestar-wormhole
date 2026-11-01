package ui

import (
	"encoding/json/v2"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

func TestSelectedDeviceUIStatus(t *testing.T) {
	for _, tc := range []struct{ serial, model, label string }{
		{"serial-sentinel-secret-1234", "S50 Pro", "S50 Pro serial-sentinel-secret-1234"},
		{"serial-sentinel-secret-1234", "Seestar S50 Pro", "Seestar S50 Pro serial-sentinel-secret-1234"},
		{"tiny", "S30", "S30 tiny"},
		{"serial-sentinel-secret-1234", "private-model", "private-model serial-sentinel-secret-1234"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			s := testServer(t)
			s.controller.done = make(chan struct{})
			s.controller.status = proxy.NewStatus()
			read := func() string {
				w := httptest.NewRecorder()
				s.ServeHTTP(w, request(s, "GET", "/api/status", ""))
				if w.Code != 200 {
					t.Fatal(w.Code)
				}
				var view struct {
					SelectedDevice string `json:"selected_device"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
					t.Fatal(err)
				}
				return view.SelectedDevice
			}
			if read() != "" {
				t.Fatal("selection shown before success")
			}
			s.controller.status.SetSelectedDevice(cloudhttp.Device{Serial: tc.serial, Model: tc.model})
			if got := read(); got != tc.label {
				t.Fatalf("selected=%q want=%q", got, tc.label)
			}
			s.controller.done = nil
			if read() != "" {
				t.Fatal("stale selection shown outside run")
			}
		})
	}
}

func TestSelectedDeviceUIBinding(t *testing.T) {
	html, _ := assets.ReadFile("web/index.html")
	js, _ := assets.ReadFile("web/app.js")
	if strings.Contains(string(html), `id="selected-device"`) || strings.Contains(string(js), "Telescope: ") {
		t.Fatal("selected telescope must be folded into status")
	}
	if strings.Count(string(js), "text('status', statusText);") != 1 {
		t.Fatal("status must be written once per update")
	}
}

func TestRunningRowHasOnlyStop(t *testing.T) {
	html, _ := assets.ReadFile("web/index.html")
	js, _ := assets.ReadFile("web/app.js")
	for _, obsolete := range []string{"selected-device", "change-telescope", "Stops the session.", "Stopped. Choose"} {
		if strings.Contains(string(html)+string(js), obsolete) {
			t.Fatalf("obsolete running control or copy: %s", obsolete)
		}
	}
	if !strings.Contains(string(html), `<p id="feedback"></p></div>`+"\n"+`<div id="primary-action"><button id="start"`) {
		t.Fatal("Stop must directly follow status announcements")
	}
	s := testServer(t)
	if w := doPost(s, "/api/change-telescope", `{}`); w.Code != 404 {
		t.Fatalf("removed action status=%d", w.Code)
	}
}
