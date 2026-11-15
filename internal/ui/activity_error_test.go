package ui

import (
	"encoding/json/v2"
	"errors"
	"testing"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// trailJSON is the status's events and failures as the page receives them.
func trailJSON(t *testing.T, l *eventLog) string {
	t.Helper()
	events, failures, _, _ := l.trail()
	encoded, err := json.Marshal(map[string]any{"events": events, "failures": failures})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// Activity shows each event's error text after its message, markup as
// text; an event without one keeps its line.
func TestActivityErrorText(t *testing.T) {
	l := &eventLog{storage: testStorage(t)}
	l.add("Connecting")
	l.addFailure(errorCopy("connection"), diagnostic.Wrap("connect", "", errors.New("<b>dial tcp 192.0.2.10:443</b>: connection refused")))
	l.addOutcome(copyapp.Outcome{Source: diagnostic.SourceBrowse, Op: diagnostic.OpConnect, Seq: 1,
		Err: errors.New("open /Users/me/Seestar/a.fit: permission denied")})
	runPageRegression(t, "activity-error-test.js", "checkActivityError("+trailJSON(t, l)+")")
}
