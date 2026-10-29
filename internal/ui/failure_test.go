package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestFailureEventsRetainSafeCause(t *testing.T) {
	log := &eventLog{storage: testStorage(t)}
	cause := diagnostic.Wrap("rdt", "timeout", errors.New("PRIVATE_DEPENDENCY_TEXT"))
	log.addFailure(errorCopy("connection"), cause)
	events, _, _ := log.snapshot()
	if len(events) != 1 || events[0].Phase != "rdt" || events[0].Reason != "timeout" {
		t.Fatalf("events: %+v", events)
	}
	b, err := os.ReadFile(filepath.Join(log.storage.root, "gui.log"))
	if err != nil || !strings.Contains(string(b), `"reason":"timeout"`) || strings.Contains(string(b), "PRIVATE_DEPENDENCY_TEXT") {
		t.Fatalf("log: %s %v", b, err)
	}
}
func TestStartupBusyHasReleaseRecoveryCopy(t *testing.T) {
	for _, err := range []error{cloud.ErrBusy, session.ErrBusy, session.ErrUncertain, session.ErrReleaseAbandoned} {
		e := actionError(err)
		if e.Code != "busy" || errorCopy(e.Code) != "The telescope is still releasing the previous session. Try again in a minute." {
			t.Fatalf("busy: %+v", e)
		}
	}
}
func TestStopIgnoresQueuedReady(t *testing.T) {
	c := &controller{stopping: true, log: &eventLog{}}
	c.recordProgress(uiText("Ready"))
	events, _, _ := c.log.snapshot()
	if len(events) != 0 {
		t.Fatalf("Stop announced Connected: %+v", events)
	}
	c.stopping = false
	c.recordProgress(uiText("Ready"))
	events, _, _ = c.log.snapshot()
	if len(events) != 1 {
		t.Fatal("Ready lost before Stop")
	}
}
