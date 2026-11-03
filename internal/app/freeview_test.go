package app

import (
	"errors"
	"io/fs"
	"reflect"
	"testing"

	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/plan"
)

// The wording of the states a run reaches rarely, from their status.
func TestFreeWording(t *testing.T) {
	refused := &fs.PathError{Op: "remove", Path: "MyWorks/b.jpg", Err: fs.ErrPermission}
	for _, c := range []struct {
		name string
		st   freeStatus
		want freeView
	}{
		{"counted", freeStatus{state: frChecking, files: 1204, hashed: 1500, toHash: 3_000_000},
			freeView{Title: "Checking 1,204 files…", Progress: &freeBar{1500, 3_000_000, "Checking your copies: 1.5 KB of 3.0 MB"}}},
		{"nothing", freeStatus{state: frNothing, set: Settings{OlderThanDays: 7}, preview: &engine.DeleteResult{}},
			freeView{Title: "Nothing to delete yet.", Text: "Files become eligible once they're copied and older than 7 days."}},
		{"nothing any age", freeStatus{state: frNothing, preview: &engine.DeleteResult{}},
			freeView{Title: "Nothing to delete yet.", Text: "Files become eligible once they're copied."}},
		{"nothing trusted", freeStatus{state: frNothing, set: Settings{OlderThanDays: 1, TrustState: true}, preview: &engine.DeleteResult{}},
			freeView{Title: "Nothing to delete yet.", Text: "Files become eligible once seestar-copy's records show them copied and older than 1 day."}},
		{"trusted", freeStatus{state: frPreview, set: Settings{TrustState: true}, id: 2,
			preview: &engine.DeleteResult{Files: []engine.DeleteFile{{Path: "a.jpg", Size: 3, Outcome: engine.FileDeleted}}}},
			freeView{Title: "1 file (3 B) matches the saved copy records.", Sub: "Any age · Your copies were not checked.", ID: 2, Trust: true,
				Groups: []freeGroup{{Folder: ".", Name: "Share", Bytes: 3, Size: "3 B", Files: []freeFile{{"a.jpg", "3 B"}}}}}},
		{"done with kept", freeStatus{state: frDone, set: Settings{OlderThanDays: 7}, deleted: 1, bytes: 2000, result: engine.DeleteResult{Files: []engine.DeleteFile{
			{Path: "a", Outcome: engine.FileDeleted}, {Path: "b", Outcome: engine.FileKept, Reason: engine.KeepRaced},
			{Path: "c", Outcome: engine.FileFailed, Reason: "open: no such file"}, {Path: "d", Outcome: engine.FileRecent}}}},
			freeView{Title: "Deleted 1 file.", Text: "Freed 2.0 KB on the telescope.", Kept: "2 files were kept. 1 file couldn't be checked.",
				KeptList: []failure{{"b", "kept: changed on the telescope since it was copied"}, {"c", "couldn't be checked: open: no such file"},
					{"d", "kept: newer than 7 days"}}}},
		{"deleting", freeStatus{state: frDeleting, deleted: 2, total: 5, current: "MyWorks/b.jpg"},
			freeView{Title: "Deleting 3 of 5…", Progress: &freeBar{Done: 2, Total: 5}, File: "Last deleted: MyWorks/b.jpg"}},
		{"deleting the first", freeStatus{state: frDeleting, total: 5},
			freeView{Title: "Deleting 1 of 5…", Progress: &freeBar{Done: 0, Total: 5}}},
		{"failed after some", freeStatus{state: frFailed, deleted: 2, err: refused, result: engine.DeleteResult{Files: []engine.DeleteFile{
			{Path: "MyWorks/b.jpg", Outcome: engine.FileFailed, Reason: refused.Error()}}}},
			freeView{Title: "Stopped: the telescope refused to delete MyWorks/b.jpg (permission denied).", Text: "2 files were deleted before this; nothing after it."}},
		{"no folder", freeStatus{state: frCheckFail, set: Settings{Folder: "/x/Seestar"}, err: engine.ErrNoDest},
			freeView{Title: "Couldn't finish checking files: /x/Seestar isn't there.", Text: noDest}},
		{"failed to connect", freeStatus{state: frFailed, err: errors.New("connect to x: no route")},
			freeView{Title: "Stopped: connect to x: no route.", Text: "No files were deleted."}},
	} {
		got := c.st.view()
		c.want.State = c.st.state
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, c.want)
		}
	}
}

// Each keep reason has a plain wording.
func TestKeptReason(t *testing.T) {
	for reason, want := range map[string]string{
		plan.KeepNotState:     "not copied yet",
		plan.KeepChanged:      "changed on the telescope since it was copied",
		engine.KeepRaced:      "changed on the telescope since it was copied",
		engine.KeepMissing:    "your copy isn't in the folder",
		engine.KeepNotRegular: "your copy no longer matches",
		engine.KeepSize:       "your copy no longer matches",
		engine.KeepHash:       "your copy no longer matches",
		engine.KeepGone:       "no longer on the telescope",
		plan.KeepPath:         "name the telescope can't handle exactly",
		"name clash: a and A": "name the telescope can't handle exactly",
	} {
		if _, got := keptReason(engine.DeleteFile{Outcome: engine.FileKept, Reason: reason}, 0); got != want {
			t.Errorf("%q: got %q, want %q", reason, got, want)
		}
	}
}

func TestNum(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if got := num(n); got != want {
			t.Errorf("num(%d) = %q, want %q", n, got, want)
		}
	}
}
