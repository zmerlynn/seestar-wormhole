package plan

import (
	"path"
	"time"
)

// OldEnough reports whether a device mtime is at least limit old at now. A
// limit of zero is no limit; otherwise an mtime in the future is never old
// enough.
func OldEnough(now, mtime time.Time, limit time.Duration) bool {
	return limit == 0 || now.Sub(mtime) >= limit
}

// DeleteInput is everything delete's decisions on the device listing
// depend on.
type DeleteInput struct {
	Listing []Entry
	// State is the loaded state, or nil when no state is read
	// (--ignore-state); then no record is needed.
	State     map[string]Recorded
	Now       time.Time
	OlderThan time.Duration
	// ValidPath reports whether the share would receive a path exactly as
	// given: remote.ValidPath, the rule the remove methods apply.
	ValidPath func(string) bool
}

// DeleteKind is what delete does with a regular file.
type DeleteKind int

// The kinds of delete decision.
const (
	// Delete: the listing and the state allow it. With --dest, the local
	// copy must still prove it, and the device rechecks it before removal.
	Delete DeleteKind = iota
	// Keep: a check kept it; Reason says which, and Warn says whether the
	// reason is worth a warning.
	Keep
	// Recent: younger than the age limit. It is only counted.
	Recent
)

// The reasons a DeleteDecision keeps a file for, other than a name clash.
const (
	KeepPath     = "the share would not receive this name exactly"
	KeepNotState = "not in the state"
	KeepChanged  = "changed on the device since it was copied"
)

// DeleteDecision is delete's decision on one listed regular file.
type DeleteDecision struct {
	Kind   DeleteKind
	Reason string
	Warn   bool
}

// DecideDelete returns the decision on each listed regular file, keyed by
// path. The checks run in this order, and the first that keeps a file
// decides: its path, a name clash with another listed or recorded path,
// its age, whether it is recorded, and whether the device's size and mtime
// equal the record.
func DecideDelete(in DeleteInput) map[string]DeleteDecision {
	var listed, folders []string
	for _, e := range in.Listing {
		if e.Regular {
			listed = append(listed, e.Path)
		} else {
			folders = append(folders, e.Path)
		}
	}
	clash := clashes(listed, folders, in.State)
	out := make(map[string]DeleteDecision, len(listed))
	for _, e := range in.Listing {
		if e.Regular {
			out[e.Path] = in.decide(e, clash[e.Path])
		}
	}
	return out
}

func (in DeleteInput) decide(e Entry, clash string) DeleteDecision {
	keep := func(reason string, warn bool) DeleteDecision {
		return DeleteDecision{Kind: Keep, Reason: reason, Warn: warn}
	}
	if !in.ValidPath(e.Path) {
		return keep(KeepPath, true)
	}
	if clash != "" {
		return keep(clash, true)
	}
	if !OldEnough(in.Now, e.ModTime, in.OlderThan) {
		return DeleteDecision{Kind: Recent}
	}
	if in.State == nil {
		return DeleteDecision{Kind: Delete}
	}
	rec, ok := in.State[e.Path]
	switch {
	case !ok:
		return keep(KeepNotState, false)
	case rec.Size != e.Size || !rec.ModTime.Equal(e.ModTime):
		return keep(KeepChanged, true)
	}
	return DeleteDecision{Kind: Delete}
}

// Folders decides which listed folders delete removes: a folder that held
// entries before the run and that the run left empty. The share root is
// never one, and a folder that was already empty stays.
//
// Delete goes through the listing in reverse, so it reaches a folder after
// everything in it. It calls Kept for every entry it does not remove, and
// asks Emptied of each folder when it reaches it.
type Folders struct {
	had, kept map[string]bool
}

// NewFolders returns the Folders of a listing. leftOut holds the folders
// with something in them that the listing left out; they had entries, and
// those entries are kept.
func NewFolders(listing []Entry, leftOut map[string]bool) *Folders {
	f := &Folders{had: map[string]bool{}, kept: map[string]bool{}}
	for _, e := range listing {
		f.had[path.Dir(e.Path)] = true
	}
	for dir := range leftOut {
		f.had[dir] = true
		f.kept[dir] = true
	}
	return f
}

// Kept records that p stays, so its folder is not empty.
func (f *Folders) Kept(p string) {
	f.kept[path.Dir(p)] = true
}

// Emptied reports whether the folder dir held entries and all of them
// were removed. It is false for the root.
func (f *Folders) Emptied(dir string) bool {
	return dir != "." && f.had[dir] && !f.kept[dir]
}
