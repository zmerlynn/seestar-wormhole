package app

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// A clear's outcome is numbered under the lock that orders clears: a clear
// refused because another copy held the history, held just as it numbers
// its outcome, can't take a number after a later clear that succeeded.
func TestClearHistoryNumberedWhereItEnds(t *testing.T) {
	e := newChangeEnv(t)
	s := e.m.s
	s.mu.Lock()
	history, folder := s.file.settings.historyDir(s.stateDir), s.file.settings.Folder
	s.mu.Unlock()
	st, err := state.Open(history, folder, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// The first draw after arming blocks until release; later draws pass
	// straight through (a sync.Once would hold them too).
	var armed atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	s.rep.drawing = func() {
		if armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
	}
	armed.Store(true)
	first := make(chan int, 1)
	go func() { first <- e.post("/copy/api/clear", `{}`).Code }()
	<-entered
	st.Close()
	var secondCode int
	second := make(chan struct{})
	go func() { defer close(second); secondCode = e.post("/copy/api/clear", `{}`).Code }()
	// A second clear that can't wait for the first is done well within
	// this; one that waits on the first's lock is not.
	select {
	case <-second:
		t.Log("the second clear ended while the first was numbering its outcome")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	if code := <-first; code != 409 {
		t.Errorf("first clear = %d, want 409", code)
	}
	<-second
	if secondCode != 200 {
		t.Errorf("second clear = %d, want 200", secondCode)
	}

	e.reported.Lock()
	defer e.reported.Unlock()
	var failed, cleared []Outcome
	for _, o := range e.outcomes {
		if o.Source != diagnostic.SourceCopy || o.Op != diagnostic.OpClearHistory {
			continue
		}
		if o.Failed() {
			failed = append(failed, o)
		} else {
			cleared = append(cleared, o)
		}
	}
	if len(failed) != 1 || len(cleared) != 1 {
		t.Fatalf("clear outcomes: failed %+v, cleared %+v", failed, cleared)
	}
	if failed[0].Seq >= cleared[0].Seq {
		t.Errorf("the refused clear is numbered %d, after the later clear's %d", failed[0].Seq, cleared[0].Seq)
	}
}
