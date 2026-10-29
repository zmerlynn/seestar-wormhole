package ui

import (
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"strings"
	"testing"
)

func TestPortErrorsExplainAcceptedSyntax(t *testing.T) {
	want := "Use ports 1–65535, with no duplicates. Leave a port empty to turn off its service."
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
	want := "Session release could not be confirmed. If Seestar is occupied, wait and try Connect again."
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
	if errorCopy("ports_empty") != "Enter a local port for at least one service in Advanced." {
		t.Fatal("empty listener error does not explain recovery")
	}
}

func TestOwnerCopy(t *testing.T) {
	b, _ := assets.ReadFile("web/index.html")
	for _, want := range []string{"Your remote Seestar, as if it were on your network.", "Enter an IPv4 address assigned to this computer.", "Let the Seestar app find your Seestar", "All networks</option>", "This computer only"} {
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

func TestKeychainRecoveryMatchesTwoPasswordStates(t *testing.T) {
	for _, code := range []string{"password_read", "password_missing", "keychain_timeout"} {
		copy := errorCopy(code)
		if strings.Contains(copy, "for this launch") {
			t.Fatalf("%s offers typing into disabled saved input: %s", code, copy)
		}
	}
}

func TestOccupiedCopyDistinguishesRetryPaths(t *testing.T) {
	if got := errorCopy("busy"); got != "Seestar is occupied. If another remote session is open, disconnect it." {
		t.Fatal(got)
	}
	if got := uiText("Busy — waiting for the other session"); got != "Seestar is occupied. Retrying automatically." {
		t.Fatal(got)
	}
	for _, message := range []string{"App opened.", "App could not start.", uiText("Ready"), uiText("Busy — waiting for the other session"), uiText("Reconnecting — waiting for cleanup/retry"), "Stopped. Seestar may still be releasing the session."} {
		if eventCode(message) != "lifecycle" {
			t.Fatalf("lifecycle event dropped: %s", message)
		}
	}
}
