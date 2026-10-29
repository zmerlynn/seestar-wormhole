package ui

import (
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"strings"
	"testing"
)

func TestPortErrorsExplainAcceptedSyntax(t *testing.T) {
	want := "Enter one computer-port:telescope-port pair per line. Computer ports must be 0–65535 and telescope ports 1–65535. Use each telescope port and each nonzero computer port only once."
	for _, ports := range [][]string{{"malformed"}, {"65536:80"}, {"1:0"}, {"1234:80", "1234:445"}} {
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
	if len(events) != 1 || !strings.Contains(events[0], "\tsignin\t") {
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
		s.Ports = append(s.Ports, fmt.Sprintf("0:%d", p.Device))
	}
	_, _, err = s.validate()
	if err == nil || actionError(err).Code != "ports_empty" || actionError(err).Field != "ports" {
		t.Fatal("empty listener set lacks its ports error", err)
	}
	if errorCopy("ports_empty") != "Enable at least one service. In Advanced, remove a port override that turns a service off." {
		t.Fatal("empty listener error does not explain recovery")
	}
}

func TestOwnerCopy(t *testing.T) {
 b, _ := assets.ReadFile("web/index.html")
 for _, want := range []string{"Use your remote Seestar through the Telescope Network, as if it were nearby.", "Enter an IPv4 address assigned to this computer.", "Let the Seestar app find this telescope", "All networks</option>", "This computer only (the Seestar app can't connect)"} {
  if !strings.Contains(string(b), want) { t.Fatalf("missing copy %q", want) }
 }
 if strings.Contains(string(b), "The telescope keeps its own site and clock.") || strings.Contains(string(b), "phone app") { t.Fatal("obsolete copy") }
 if !strings.Contains(uiText("Ready"), "Connected") { t.Fatal("ready copy") }
}
