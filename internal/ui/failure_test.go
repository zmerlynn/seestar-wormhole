package ui

import (
	"context"
	"encoding/json/v2"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"net/http/httptest"
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
		if e.Code != "busy" || errorCopy(e.Code) != "Seestar is occupied. If another remote session is open, disconnect it." {
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

func TestControllerFailureEventReachesStatus(t *testing.T) {
	s := testServer(t)
	s.controller.log = &eventLog{storage: s.storage}
	release := make(chan struct{})
	s.controller.run = func(context.Context, Settings, cloud.Credentials, *proxy.Status) error {
		<-release
		return diagnostic.Wrap("rdt", "timeout", errors.New("PRIVATE_DEPENDENCY_TEXT"))
	}
	if err := s.controller.start(testSettings(), testPassword); err != nil {
		t.Fatal(err)
	}
	s.controller.mu.Lock()
	done := s.controller.done
	s.controller.mu.Unlock()
	close(release)
	<-done
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request(s, "GET", "/api/status", ""))
	var got struct {
		Events []event `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	last := got.Events[len(got.Events)-1]
	if last.Phase != "rdt" || last.Reason != "timeout" || last.Code != "connection" {
		t.Fatalf("status failure: %+v", last)
	}
	if strings.Contains(w.Body.String(), "PRIVATE_DEPENDENCY_TEXT") {
		t.Fatal("dependency text escaped to page")
	}
}
