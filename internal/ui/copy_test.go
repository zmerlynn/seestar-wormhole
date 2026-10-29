package ui

import (
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"strings"
	"testing"
)

func TestPortErrorsExplainAcceptedSyntax(t *testing.T) {
	want := "Enter a local port from 1 to 65535, or leave it empty to turn the service off. Use each local port only once."
	for _, ports := range [][]proxyrun.Port{{{Device: 999}}, {{Local: 1234, Device: 80}, {Local: 1234, Device: 445}}, {{Device: 80}, {Device: 80}}} {
		s := testSettings()
		s.Ports = ports
		_, _, err := s.validate()
		if err == nil || actionError(err).Code != "ports" {
			t.Fatal("incorrect field diagnosis", err)
		}
		if got := errorCopy(actionError(err).Code); got != want {
			t.Fatal("misleading ports recovery:", got)
		}
	}
}

func TestReleaseUncertaintyOffersBusyRetryWithoutCountdown(t *testing.T) {
	want := "Session release could not be confirmed. If Connect fails as busy, wait a minute and try again."
	if errorCopy("release") != want {
		t.Fatal("release uncertainty copy promises a fixed wait")
	}
}

func TestErrorCodesBelongInDiagnosticEvents(t *testing.T) {
	log := &eventLog{}
	log.add(errorCopy("signin"))
	events, _, _ := log.snapshot()
	if len(events) != 1 || events[0].Code != "signin" {
		t.Fatal("diagnostic event lost the error classification")
	}
}

func TestAllServicesDisabledExplainsRecovery(t *testing.T) {
	defaults, err := proxyrun.MergePorts(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := testSettings()
	s.Ports = nil
	for _, p := range defaults {
		s.Ports = append(s.Ports, proxyrun.Port{Device: p.Device})
	}
	_, _, err = s.validate()
	if err == nil || actionError(err).Code != "ports_empty" || actionError(err).Field != "ports" {
		t.Fatal("empty listener set lacks its ports error", err)
	}
	if errorCopy("ports_empty") != "Enable at least one service by entering a local port in Advanced." {
		t.Fatal("empty listener error does not explain recovery")
	}
}

func TestOwnerCopy(t *testing.T) {
	b, _ := assets.ReadFile("web/index.html")
	for _, want := range []string{"Use your remote Seestar through the Telescope Network, as if it were nearby.", "Enter an IPv4 address assigned to this computer.", "Let the Seestar app find this telescope", "All networks</option>", "This computer only (the Seestar app can't connect)"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("missing copy %q", want)
		}
	}
	if strings.Contains(string(b), "The telescope keeps its own site and clock.") || strings.Contains(string(b), "phone app") {
		t.Fatal("obsolete copy")
	}
	if !strings.Contains(uiText("Ready"), "Connected") {
		t.Fatal("ready copy")
	}
}
