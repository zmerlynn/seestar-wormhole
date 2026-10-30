package ui

import (
	"runtime"
	"strings"
	"testing"
)

// Review-only dependency experiment. Hold the event writer at its own mutex,
// then observe whether a progress publisher also owns the Stop/status mutex.
func TestLogWriterDoesNotHoldControlLock(t *testing.T) {
	c := testController(nil)
	c.log = &eventLog{}
	c.log.diskMu.Lock()
	done := make(chan struct{})
	go func() { c.recordProgress("Connected to your Seestar"); close(done) }()
	observed := false
	for range 10000 {
		stack := make([]byte, 64<<10)
		n := runtime.Stack(stack, true)
		text := string(stack[:n])
		if strings.Contains(text, "(*controller).recordProgress") {
			observed = true
			break
		}
		runtime.Gosched()
	}
	free := c.mu.TryLock()
	if free {
		c.mu.Unlock()
	}
	c.log.diskMu.Unlock()
	<-done
	if !observed {
		t.Fatal("probe did not observe the logger wait")
	}
	if !free {
		t.Fatal("event writer wait holds the Stop/status mutex")
	}
}
