package ui

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
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

// The summary takes an unauthorized error's text with the account email,
// in any case, replaced; the page's Activity keeps it.
func TestSummaryMasksAccount(t *testing.T) {
	email := testSettings().Email
	l := &eventLog{storage: testStorage(t)}
	l.addFailure(errorCopy("signin"), fmt.Errorf("authentication for %s: %w", email, cloud.ErrUnauthorized))
	l.addOutcome(copyapp.Outcome{Source: diagnostic.SourceBrowse, Op: diagnostic.OpConnect, Seq: 1,
		Err: fmt.Errorf("authentication for %s: %w", strings.ToUpper(email), cloud.ErrUnauthorized)})
	runPageRegression(t, "activity-error-test.js", "checkSummaryAccount("+trailJSON(t, l)+","+strconv.Quote(email)+")")
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
