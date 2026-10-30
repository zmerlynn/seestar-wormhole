package ui

import (
	"encoding/json/v2"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
)

func TestEffectiveDiagnosticSettings(t *testing.T) {
	s := testServer(t)
	s.version = "synthetic-version"
	s.controller.done = make(chan struct{})
	s.controller.status = proxy.NewStatus()
	run := testSettings()
	run.Bind, run.Discovery, run.Idle = "PRIVATE_ADDRESS", false, "1h30m"
	run.Serial, run.Model, run.Email = "", "", "PRIVATE_EMAIL@example.invalid"
	run.Ports = []proxyrun.Port{{Device: 80}, {Device: 445, Local: 1234}}
	s.controller.runSettings = run
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request(s, "GET", "/api/status", ""))
	var got struct {
		Version   string             `json:"version"`
		Effective diagnosticSettings `json:"effective"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != s.version || got.Effective.Listen != "other" || got.Effective.Discovery || got.Effective.Idle.Mode != "on_demand" || got.Effective.Idle.Minutes != 90 || got.Effective.Ports[80] != 0 || got.Effective.Ports[445] != 1234 || len(got.Effective.Ports) != 10 || got.Effective.Ports[4700] != 4700 {
		t.Fatalf("effective: %+v", got)
	}
	b, _ := json.Marshal(got.Effective)
	if strings.Contains(string(b), "PRIVATE_") {
		t.Fatalf("private fields: %s", b)
	}
	log := &eventLog{}
	log.add("App opened.")
	events, _, _ := log.snapshot()
	if len(events) != 1 || events[0].Time.Location() != time.UTC || events[0].Message != "App opened." {
		t.Fatalf("events: %+v", events)
	}
}
