package ui

import (
	"encoding/json/v2"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"net/http/httptest"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

func TestStatusMappingsUseSharedMergerAndRunningSnapshot(t *testing.T) {
	s := testServer(t)
	for _, active := range []bool{false, true} {
		settings := testSettings()
		settings.Ports = []proxyrun.Port{{Local: 0, Device: 80}, {Local: 1234, Device: 445}}
		if active {
			s.controller.done = make(chan struct{})
			s.controller.status = proxy.NewStatus()
			s.controller.runSettings = settings
		} else {
			s.settings = settings
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, request(s, "GET", "/api/status", ""))
		var got struct {
			Mappings [][2]uint16 `json:"mappings"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Mappings) != 9 {
			t.Fatalf("active=%v mappings=%v", active, got.Mappings)
		}
		if got.Mappings[1] != ([2]uint16{1234, 445}) {
			t.Fatal("shared override not projected", got.Mappings)
		}
		for _, p := range got.Mappings {
			if p[1] == 80 {
				t.Fatal("disabled service projected")
			}
		}
		// The running projection must retain its accepted snapshot after page edits.
		s.settings = testSettings()
	}
}
