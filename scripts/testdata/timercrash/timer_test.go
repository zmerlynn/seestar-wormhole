package timercrash

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func timers() {
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			timer := time.NewTimer(time.Hour)
			defer timer.Stop()
			for range 10000 {
				timer.Stop()
				timer.Reset(0)
				select {
				case <-timer.C:
				}
			}
		})
	}
	wg.Wait()
}

func TestBubble(t *testing.T)    { synctest.Test(t, func(t *testing.T) { timers() }) }
func TestRealClock(t *testing.T) { timers() }
