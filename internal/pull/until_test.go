package pull

import (
	"testing"
	"time"
)

func TestUntilBounds(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		until  string
		settle time.Duration
		upper  time.Time
	}{
		{"2h", time.Minute, now.Add(-2 * time.Hour)},
		{"2026-10-13T06:15:00.125-05:00", time.Minute, time.Date(2026, 10, 13, 11, 15, 0, 125000000, time.UTC)},
		{"2h", 3 * time.Hour, now.Add(-3 * time.Hour)},
		{"2026-10-13T13:00:00Z", time.Minute, now.Add(-time.Minute)},
	} {
		c := DefaultConfig()
		c.Until = tc.until
		c.Settle = tc.settle
		b, err := c.bounds(now, time.Time{})
		if err != nil || !b.Upper.Equal(tc.upper) {
			t.Fatalf("%q: bounds=%+v want upper=%s err=%v", tc.until, b, tc.upper, err)
		}
	}
}

func TestUntilInclusiveAndCheckpointCap(t *testing.T) {
	c, d, src, root, now := fixture(t)
	until := now.Add(-time.Hour)
	c.Until = until.Format(time.RFC3339Nano)
	putFile(t, root, "Dark/before.fit", "before", until.Add(-time.Second))
	putFile(t, root, "Dark/equal.fit", "equal", until)
	putFile(t, root, "Dark/after.fit", "after", until.Add(time.Second))
	b := runBatch(t, d, c, src, now)
	if b.Result.Copied != 2 || b.Result.Deferred != 1 || !b.Result.Bounds.Upper.Equal(until) {
		t.Fatalf("inclusive=%+v", b.Result)
	}
	if _, ok := d.State.Files["Dark/equal.fit"]; !ok {
		t.Fatal("inclusive equality was skipped")
	}
	if got := d.State.Selections[selectionKey(c.Only)].Checkpoint; !got.Equal(until) {
		t.Fatalf("checkpoint=%s want=%s", got, until)
	}
	// Duration-ago uses this run's supplied start; settle remains the tighter cap.
	c.Until = "30m"
	c.Settle = 2 * time.Hour
	b = runBatch(t, d, c, src, now.Add(2*time.Hour))
	if !b.Result.Bounds.Upper.Equal(now) || !d.State.Selections[selectionKey(c.Only)].Checkpoint.Equal(now) {
		t.Fatalf("settle cap=%+v", b.Result)
	}
	c.Until = until.Format(time.RFC3339Nano)
	runBatch(t, d, c, src, now.Add(3*time.Hour))
	if !d.State.Selections[selectionKey(c.Only)].Checkpoint.Equal(now) {
		t.Fatal("historical until moved an existing checkpoint")
	}
	c.Since = "all"
	c.Until = "30m"
	runBatch(t, d, c, src, now.Add(4*time.Hour))
	if !d.State.Selections[selectionKey(c.Only)].Checkpoint.Equal(now) {
		t.Fatal("explicit since advanced checkpoint")
	}
}
