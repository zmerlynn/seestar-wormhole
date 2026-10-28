package ui

import (
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
