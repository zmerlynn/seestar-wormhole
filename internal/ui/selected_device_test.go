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
		{"serial-sentinel-secret-1234", "S50 Pro", "S50 Pro ***1234"},
		{"serial-sentinel-secret-1234", "Seestar S50 Pro", "Seestar S50 Pro ***1234"},
		{"tiny", "S30", "S30 ***"},
		{"serial-sentinel-secret-1234", "private-model", "[unknown model] ***1234"},
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
				if strings.Contains(w.Body.String(), tc.serial) || strings.Contains(w.Body.String(), "private-model") {
					t.Fatal("private identity escaped UI")
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
	if !strings.Contains(string(html), `<p id="selected-device" hidden></p>`) {
		t.Fatal("selected telescope has no display")
	}
	// Pin the textContent binding: the value is text, never HTML or settings input.
	if !strings.Contains(string(js), "text('selected-device',s.selected_device ? 'Selected: ' + s.selected_device : '');") ||
		!strings.Contains(string(js), "$('selected-device').hidden = !s.selected_device;") {
		t.Fatal("selected telescope is not rendered")
	}
}
