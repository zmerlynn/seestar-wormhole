package plan

import (
	"testing"
	"time"
)

func TestFileStatus(t *testing.T) {
	mtime := time.Date(2026, 11, 1, 3, 10, 0, 123456789, time.UTC)
	e := Entry{Path: "MyWorks/a.fit", Size: 16000000, ModTime: mtime, Regular: true}
	tests := []struct {
		name     string
		rec      Recorded
		recorded bool
		want     string
	}{
		{"not recorded", Recorded{}, false, "new"},
		{"not recorded, matching zero record", Recorded{Size: e.Size, ModTime: mtime}, false, "new"},
		{"same size and instant", Recorded{Size: e.Size, ModTime: mtime}, true, "copied"},
		{"same instant in another zone", Recorded{Size: e.Size, ModTime: mtime.In(time.FixedZone("x", -7*3600))}, true, "copied"},
		{"size one more", Recorded{Size: e.Size + 1, ModTime: mtime}, true, "changed"},
		{"size one less", Recorded{Size: e.Size - 1, ModTime: mtime}, true, "changed"},
		{"mtime one nanosecond later", Recorded{Size: e.Size, ModTime: mtime.Add(time.Nanosecond)}, true, "changed"},
		{"mtime one nanosecond earlier", Recorded{Size: e.Size, ModTime: mtime.Add(-time.Nanosecond)}, true, "changed"},
	}
	for _, tt := range tests {
		if got := FileStatus(e, tt.rec, tt.recorded); got != tt.want {
			t.Errorf("%s: FileStatus = %q, want %q", tt.name, got, tt.want)
		}
	}
}
