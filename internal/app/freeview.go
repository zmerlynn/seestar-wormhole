package app

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/plan"
)

// The Free up space tab's states, as the page names them. Confirm is the
// page's own dialog over Preview.
const (
	frStart     = "start"
	frChecking  = "checking"
	frCheckFail = "checkfail"
	frPreview   = "preview"
	frNothing   = "nothing" // a preview with nothing to delete
	frDeleting  = "deleting"
	frDone      = "done"
	frStopped   = "stopped"
	frFailed    = "failed"
)

// freeStatus is the run's state, for the page.
type freeStatus struct {
	state   string
	changed bool // Start, after a Save or a Clear history changed what a preview showed
	stale   bool // a Save changed the inputs of the running check
	set     Settings
	// Checking.
	files          int // Checked
	hashed, toHash int64
	// Preview and Nothing: the preview, and its number.
	preview *engine.DeleteResult
	id      int
	// Deleting, and how it ended.
	total    int // the files Only holds
	deleted  int
	bytes    int64
	current  string // the last file deleted
	stopping bool
	result   engine.DeleteResult
	err      error
}

// freeView is the Free up space tab's state and its text, for the page.
type freeView struct {
	State    string      `json:"state"`
	Changed  bool        `json:"changed,omitempty"`
	Title    string      `json:"title,omitempty"` // also the status region's text
	Text     string      `json:"text,omitempty"`
	Sub      string      `json:"sub,omitempty"`
	Progress *freeBar    `json:"progress,omitempty"`
	File     string      `json:"file,omitempty"`
	Stopping bool        `json:"stopping,omitempty"`
	ID       int         `json:"id,omitempty"` // the preview's
	Groups   []freeGroup `json:"groups,omitempty"`
	Trust    bool        `json:"trust,omitempty"` // the preview's proof is the records alone
	Kept     string      `json:"kept,omitempty"`  // "Kept: …" or the Done counts
	KeptList []failure   `json:"kept_list,omitempty"`
}

type freeBar struct {
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
	Text  string `json:"text,omitempty"`
}

// freeGroup is one folder of a preview's files to delete.
type freeGroup struct {
	Folder string     `json:"folder"` // the key the page posts back
	Name   string     `json:"name"`
	Bytes  int64      `json:"bytes"`
	Size   string     `json:"size"`
	Files  []freeFile `json:"files"`
}

type freeFile struct {
	Path string `json:"path"`
	Size string `json:"size"`
}

// view words st for the page.
func (st *freeStatus) view() freeView {
	v := freeView{State: st.state, Changed: st.changed}
	days := st.set.OlderThanDays
	switch st.state {
	case frChecking:
		v.Title = "Checking files…"
		if st.files > 0 {
			v.Title = "Checking " + many(st.files, "file", "files") + "…"
		}
		if st.toHash > 0 {
			v.Progress = &freeBar{st.hashed, st.toHash, "Checking your copies: " + size(float64(st.hashed)) + " of " + size(float64(st.toHash))}
		}
	case frCheckFail:
		v.Title = "Couldn't finish checking files: " + st.err.Error() + "."
		v.Text = "Preview again to retry."
		if errors.Is(st.err, engine.ErrNoDest) {
			v.Title = "Couldn't finish checking files: " + st.set.Folder + " isn't there."
			v.Text = noDest
		}
	case frPreview:
		n, bytes := 0, int64(0)
		var groups []freeGroup
		for _, d := range st.preview.Files {
			if d.Outcome != engine.FileDeleted {
				continue
			}
			n++
			bytes += d.Size
			dir := path.Dir(d.Path)
			i := slices.IndexFunc(groups, func(g freeGroup) bool { return g.Folder == dir })
			if i < 0 {
				name := dir
				if dir == "." {
					name = "Share"
				}
				groups = append(groups, freeGroup{Folder: dir, Name: name})
				i = len(groups) - 1
			}
			g := &groups[i]
			g.Bytes += d.Size
			g.Files = append(g.Files, freeFile{d.Path, size(float64(d.Size))})
		}
		slices.SortFunc(groups, func(a, b freeGroup) int { return strings.Compare(a.Folder, b.Folder) })
		for i := range groups {
			g := &groups[i]
			g.Size = size(float64(g.Bytes))
			slices.SortFunc(g.Files, func(a, b freeFile) int { return strings.Compare(a.Path, b.Path) })
		}
		v.Groups, v.ID, v.Trust = groups, st.id, st.set.TrustState
		v.Title = fmt.Sprintf("%s (%s) are copied and safe to delete.", many(n, "file", "files"), size(float64(bytes)))
		if n == 1 {
			v.Title = "1 file (" + size(float64(bytes)) + ") is copied and safe to delete."
		}
		if st.set.TrustState {
			v.Title = fmt.Sprintf("%s (%s) match the saved copy records.", many(n, "file", "files"), size(float64(bytes)))
			if n == 1 {
				v.Title = "1 file (" + size(float64(bytes)) + ") matches the saved copy records."
			}
		}
		age := "Any age"
		if days > 0 {
			age = "Only files older than " + count(days, "day", "days")
		}
		v.Sub = age + " · your copies checked"
		if st.set.TrustState {
			v.Sub = age + " · Your copies were not checked."
		}
		v.Kept, v.KeptList = keptSummary(st.preview.Files, days)
	case frNothing:
		v.Title = "Nothing to delete yet."
		copied := "once they're copied"
		if st.set.TrustState {
			copied = "once seestar-copy's records show them copied"
		}
		if days > 0 {
			v.Text = "Files become eligible " + copied + " and older than " + count(days, "day", "days") + "."
		} else {
			v.Text = "Files become eligible " + copied + "."
		}
		v.Kept, v.KeptList = keptSummary(st.preview.Files, days)
	case frDeleting:
		v.Title = fmt.Sprintf("Deleting %s of %s…", num(min(st.deleted+1, st.total)), num(st.total))
		v.Progress = &freeBar{Done: int64(st.deleted), Total: int64(st.total)}
		// The engine reports a file once it is deleted, not as it starts.
		if st.current != "" {
			v.File = "Last deleted: " + st.current
		}
		v.Stopping = st.stopping
	case frDone:
		v.Title = "Deleted " + many(st.deleted, "file", "files")
		if n := st.result.Folders; n > 0 {
			v.Title += " and removed " + count(n, "empty folder", "empty folders")
		}
		v.Title += "."
		v.Text = "Freed " + size(float64(st.bytes)) + " on the telescope."
		v.Kept, v.KeptList = doneCounts(st.result.Files, days)
	case frStopped:
		v.Title = "Stopped after " + many(st.deleted, "file", "files") + " (" + size(float64(st.bytes)) + ")."
		v.Text = "Nothing else was deleted. Run it again to carry on."
	case frFailed:
		files := st.result.Files
		if n := len(files); n > 0 && files[n-1].Outcome == engine.FileFailed && files[n-1].Reason == st.err.Error() {
			v.Title = "Stopped: the telescope refused to delete " + files[n-1].Path + " (" + reason(st.err) + ")."
		} else if errors.Is(st.err, engine.ErrNoDest) {
			v.Title = "Stopped: " + st.set.Folder + " isn't there."
		} else {
			v.Title = "Stopped: " + st.err.Error() + "."
		}
		v.Text = "No files were deleted."
		if st.deleted > 0 {
			v.Text = count(st.deleted, "file was", "files were") + " deleted before this; nothing after it."
		}
	}
	return v
}

// noDest is what to do when the folder is missing and the copies prove
// the files.
const noDest = "If your copies have moved or the drive is offline, turn on \"My copies have moved or the drive is offline\" and preview again."

// keptSummary words the files a preview keeps: "Kept: 40 newer than 7
// days, 12 not copied yet", and each file with its reason.
func keptSummary(files []engine.DeleteFile, days int) (string, []failure) {
	var order []string
	counts := map[string]int{}
	var list []failure
	for _, d := range files {
		if d.Outcome == engine.FileDeleted {
			continue
		}
		short, long := keptReason(d, days)
		if counts[short] == 0 {
			order = append(order, short)
		}
		counts[short]++
		list = append(list, failure{d.Path, long})
	}
	if len(order) == 0 {
		return "", nil
	}
	parts := make([]string, len(order))
	for i, short := range order {
		parts[i] = num(counts[short]) + " " + short
	}
	slices.SortFunc(list, func(a, b failure) int { return strings.Compare(a.Path, b.Path) })
	return "Kept: " + strings.Join(parts, ", "), list
}

// keptReason words why a file is kept: a short form for the summary, and
// the reason shown beside the file.
func keptReason(d engine.DeleteFile, days int) (short, long string) {
	switch d.Outcome {
	case engine.FileRecent:
		s := "newer than " + count(days, "day", "days")
		return s, s
	case engine.FileFailed:
		return "couldn't be checked", "couldn't be checked: " + d.Reason
	}
	switch d.Reason {
	case plan.KeepNotState:
		return "not copied yet", "not copied yet"
	case plan.KeepChanged, engine.KeepRaced:
		return "changed on the telescope", "changed on the telescope since it was copied"
	case engine.KeepMissing:
		return "copy not in the folder", "your copy isn't in the folder"
	case engine.KeepNotRegular, engine.KeepSize, engine.KeepHash:
		return "copy doesn't match", "your copy no longer matches"
	case engine.KeepGone:
		return "no longer on the telescope", "no longer on the telescope"
	}
	// plan.KeepPath and the name clashes.
	return "name the telescope can't handle", "name the telescope can't handle exactly"
}

// doneCounts words what a delete with the age limit days kept or couldn't
// check: "7 files were kept. 2 files couldn't be checked.", and each file
// with its reason.
func doneCounts(files []engine.DeleteFile, days int) (string, []failure) {
	kept, failed := 0, 0
	var list []failure
	for _, d := range files {
		switch d.Outcome {
		case engine.FileDeleted:
			continue
		case engine.FileFailed:
			failed++
		default:
			kept++
		}
		_, long := keptReason(d, days)
		if d.Outcome != engine.FileFailed {
			long = "kept: " + long
		}
		list = append(list, failure{d.Path, long})
	}
	var parts []string
	if kept > 0 {
		parts = append(parts, many(kept, "file was", "files were")+" kept.")
	}
	if failed > 0 {
		parts = append(parts, many(failed, "file", "files")+" couldn't be checked.")
	}
	return strings.Join(parts, " "), list
}

// many is count with thousands separated: "1,204 files".
func many(n int, one, more string) string {
	if n == 1 {
		return "1 " + one
	}
	return num(n) + " " + more
}

// num is n with its thousands separated by commas.
func num(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
