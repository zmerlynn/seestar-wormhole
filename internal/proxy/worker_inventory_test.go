package proxy

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
)

// A label inherited by every descendant identifies one owner independently of
// other tests and runtime.NumGoroutine's process-wide scheduler estimate.
func ownedWorkerCount(t *testing.T, label, name string) int {
	t.Helper()
	var profile bytes.Buffer
	if err := pprof.Lookup("goroutine").WriteTo(&profile, 1); err != nil {
		t.Fatal(err)
	}
	total := 0
	_, groups, _ := strings.Cut(profile.String(), "\n") // profile summary precedes the first stack
	for _, group := range strings.Split(groups, "\n\n") {
		_, tail, ok := strings.Cut(group, "\n# labels: ")
		if !ok {
			continue
		}
		line, _, _ := strings.Cut(tail, "\n")
		var labels map[string]string
		if err := json.Unmarshal([]byte(line), &labels, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		if labels[label] != name {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(group, "%d @", &n); err != nil {
			t.Fatal(err)
		}
		total += n
	}
	return total
}

func TestOutageWorkerInventory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var unrelated sync.WaitGroup
		stopUnrelated := make(chan struct{})
		for i := 0; i < 27; i++ {
			unrelated.Go(func() { <-stopUnrelated })
		}
		defer func() { close(stopUnrelated); unrelated.Wait() }()
		stop, done := make(chan struct{}), make(chan struct{})
		var release sync.Once
		finish := func() { release.Do(func() { close(stop); <-done }) }
		defer finish()
		// Only the descendant keeps the label after Do returns. This is the
		// same inheritance used for the cloud and session owners in the outage.
		pprof.Do(t.Context(), pprof.Labels("outage_test", t.Name()), func(context.Context) {
			go func() { defer close(done); <-stop }()
		})
		synctest.Wait()
		if n := ownedWorkerCount(t, "outage_test", t.Name()); n != 1 {
			buf := make([]byte, 1<<20)
			used := runtime.Stack(buf, true)
			t.Errorf("owned blocked workers=%d want 1 with 27 unrelated workers\n%s", n, buf[:used])
		}
		finish()
		synctest.Wait()
		if n := ownedWorkerCount(t, "outage_test", t.Name()); n != 0 {
			t.Errorf("owned joined workers=%d want 0", n)
		}
	})
}
