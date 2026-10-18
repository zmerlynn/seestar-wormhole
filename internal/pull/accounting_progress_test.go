package pull

import (
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/rclone/rclone/fs/accounting"
)

// Progress samples stats while workers can report errors. RemoteStats must
// synchronize even fields that our numeric projection subsequently discards.
func TestProgressAccountingConcurrentErrors(t *testing.T) {
	s := accounting.NewStats(t.Context())
	var workers sync.WaitGroup
	workers.Go(func() {
		for range 1000 {
			s.Error(errors.New("synthetic failure"))
		}
	})
	for range 1000 {
		if _, err := s.RemoteStats(true); err != nil {
			t.Fatal(err)
		}
	}
	workers.Wait()
}

// Error formatting is caller code and may consult the accounting instance.
type progressStatsError struct{ stats *accounting.StatsInfo }

func (e progressStatsError) Error() string {
	e.stats.Bytes(0)
	return "synthetic callback"
}
func TestProgressAccountingErrorCallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := accounting.NewStats(t.Context())
		s.Error(progressStatsError{s})
		got, err := s.RemoteStats(true)
		if err != nil || got["lastError"] != "synthetic callback" {
			t.Fatalf("snapshot=%v err=%v", got, err)
		}
	})
}
