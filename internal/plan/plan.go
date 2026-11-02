package plan

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Entry is one item of a device listing.
type Entry struct {
	Path    string // share-relative, with forward slashes
	Size    int64
	ModTime time.Time
	Regular bool // only regular files get a decision
}

// Recorded is what the state file holds for a path.
type Recorded struct {
	Size    int64
	ModTime time.Time
}

// Local is the result of a stat of <dest>/<path> that does not follow a
// symlink (see NeedLocal). A path that does not exist has Exists false and a
// nil Err; Err is any other stat failure.
type Local struct {
	Exists  bool
	Regular bool
	Size    int64
	ModTime time.Time
	Err     error
}

// Input is everything one pass's decisions depend on.
type Input struct {
	Listing []Entry
	// FirstPass is set on the process's first pass, when there is no
	// earlier listing; Previous is then ignored. It is a flag rather than a
	// nil Previous so that a previous pass that listed nothing still counts.
	FirstPass bool
	Previous  []Entry
	State     map[string]Recorded
	// Local holds the stats of the paths NeedLocal returned, keyed by path.
	Local        map[string]Local
	Now          time.Time
	ReadyAge     time.Duration
	FirstPassAge time.Duration
	// Windows applies the rules for names Windows cannot store.
	Windows bool
}

// Kind is what a pass does with a file.
type Kind int

// The kinds of decision.
const (
	Wait    Kind = iota // not ready: too new, or changed since the previous listing
	Done                // in the state with the same size and mtime
	Copy                // not in the state: download it
	Replace             // in the state, changed on the device: download it again
	Adopt               // not in the state, an identical local file: hash it, no download
	Fail                // cannot be stored; Reason says why
)

// String returns the kind's lower-case name, for logs.
func (k Kind) String() string {
	switch k {
	case Wait:
		return "wait"
	case Done:
		return "done"
	case Copy:
		return "copy"
	case Replace:
		return "replace"
	case Adopt:
		return "adopt"
	case Fail:
		return "fail"
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// Decision is what to do with one listed path. Size and ModTime are the
// listed ones.
type Decision struct {
	Kind    Kind
	Path    string
	Size    int64
	ModTime time.Time
	// Overwrite is set on a Copy when a local regular file with a different
	// size or mtime is at the path; the copy replaces it.
	Overwrite bool
	// Reason names the rule a Fail broke.
	Reason string
}

// Counts is the number of decisions of each kind.
type Counts struct {
	Wait, Done, Copy, Replace, Adopt, Fail int
}

func (c *Counts) add(k Kind) {
	switch k {
	case Wait:
		c.Wait++
	case Done:
		c.Done++
	case Copy:
		c.Copy++
	case Replace:
		c.Replace++
	case Adopt:
		c.Adopt++
	case Fail:
		c.Fail++
	}
}

// duplicateReason is the Fail reason for a path listed more than once. A
// directory listing cannot hold two entries with one name, so this is a
// broken listing, and no entry is trusted over the other.
const duplicateReason = "path listed more than once"

// NeedLocal returns, sorted, the paths whose local stat Decide needs: the
// valid, ready regular files that are not in the state. The caller stats
// <dest>/<path> for each without following a symlink and passes the results
// in Input.Local.
//
// The path checks exclude lexical escapes only, such as ".." or an absolute
// path. The caller must also confine filesystem resolution beneath the
// destination (the copier uses an os.Root on it): os.Lstat alone rejects
// only a symlink at the leaf, and still follows a symlinked parent folder.
//
// Planning in two calls keeps the package free of I/O, and lets the caller
// stat only what can be adopted: never a path that failed the path checks,
// and never a file the state already covers, so a file the export step moved
// away is not looked for. Both calls run the same rules on the same input,
// so they agree.
func NeedLocal(in Input) []string {
	files, _, ps := in.prepare()
	var need []string
	for _, e := range files {
		if _, needLocal := in.classify(e, ps); needLocal {
			need = append(need, e.Path)
		}
	}
	slices.Sort(need)
	return need
}

// Decide returns one decision per regular file in the listing, sorted by
// path, and their counts. A path listed more than once (as anything, as long
// as one entry is a regular file) gets one Fail. So does each path whose
// name clashes with another listed or recorded one (see clashes), whatever
// its state or readiness. Directories and other non-regular entries get no
// decision, and neither does a recorded path that is not listed, but both
// take part in the clash comparison.
func Decide(in Input) ([]Decision, Counts) {
	files, dups, ps := in.prepare()
	decisions := make([]Decision, 0, len(files)+len(dups))
	for _, e := range files {
		d, needLocal := in.classify(e, ps)
		if needLocal {
			l, ok := in.Local[e.Path]
			d = withLocal(d, l, ok)
		}
		decisions = append(decisions, d)
	}
	for _, p := range dups {
		decisions = append(decisions, Decision{Kind: Fail, Path: p, Reason: withClash(duplicateReason, ps.clashes[p])})
	}
	slices.SortFunc(decisions, func(a, b Decision) int { return strings.Compare(a.Path, b.Path) })
	var counts Counts
	for _, d := range decisions {
		counts.add(d.Kind)
	}
	return decisions, counts
}

// pass is what the decisions of one pass share.
type pass struct {
	prev    map[string]Entry  // the previous listing's regular files, or nil on the first pass
	clashes map[string]string // the Fail reason of each listed path whose name clashes
}

// prepare returns the listing's unique regular files, the paths listed more
// than once, and what deciding each file needs.
func (in *Input) prepare() (files []Entry, dups []string, ps pass) {
	files, dups = uniqueRegular(in.Listing)
	listed := make([]string, 0, len(files)+len(dups))
	for _, e := range files {
		listed = append(listed, e.Path)
	}
	listed = append(listed, dups...)
	var folders []string
	for _, e := range in.Listing {
		if !e.Regular {
			folders = append(folders, e.Path)
		}
	}
	ps = pass{prev: in.previous(), clashes: clashes(listed, folders, in.State)}
	return files, dups, ps
}

// classify decides e without the local stat. When needLocal is true, the
// file is ready and not in the state, and the stat chooses between Adopt
// and Copy.
func (in *Input) classify(e Entry, ps pass) (d Decision, needLocal bool) {
	d = Decision{Path: e.Path, Size: e.Size, ModTime: e.ModTime}
	// A clash is checked before Done and readiness: no winner is picked, so
	// every spelling fails, whatever its state. A path that fails for its
	// own reason keeps it, and the clash is added.
	if reason := withClash(in.check(e), ps.clashes[e.Path]); reason != "" {
		d.Kind, d.Reason = Fail, reason
		return d, false
	}
	rec, inState := in.State[e.Path]
	switch {
	case inState && rec.Size == e.Size && rec.ModTime.Equal(e.ModTime):
		// Before the readiness check: there is nothing to wait for.
		d.Kind = Done
	case !in.ready(e, ps.prev):
		d.Kind = Wait
	case inState:
		d.Kind = Replace
	default:
		return d, true
	}
	return d, false
}

// check returns the first rule e itself breaks, or "": its path, then its
// size, then its mtime.
func (in *Input) check(e Entry) string {
	if reason := checkPath(e.Path, in.Windows); reason != "" {
		return reason
	}
	if e.Size < 0 {
		return fmt.Sprintf("negative size %d", e.Size)
	}
	return checkModTime(e.ModTime)
}

// checkModTime returns why the state file cannot record mtime t, or "".
// The state file writes it in UTC as RFC 3339, which holds only the years 0
// to 9999, and a zero mtime there means a broken record. Checking before the
// download means a file is never copied without a way to record it.
func checkModTime(t time.Time) string {
	if t.IsZero() {
		return "zero mtime, which the state file cannot record"
	}
	if y := t.UTC().Year(); y < 0 || y > 9999 {
		return fmt.Sprintf("mtime year %d is outside 0 to 9999, which the state file cannot record", y)
	}
	return ""
}

// ready reports whether e is old enough and, after the first pass, listed
// unchanged in the previous listing. A future mtime has a negative age, so
// it is never ready.
func (in *Input) ready(e Entry, prev map[string]Entry) bool {
	age := in.Now.Sub(e.ModTime)
	if age < in.ReadyAge {
		return false
	}
	if in.FirstPass {
		return age >= in.FirstPassAge
	}
	p, ok := prev[e.Path]
	return ok && p.Size == e.Size && p.ModTime.Equal(e.ModTime)
}

// withLocal completes a decision that needed the local stat l; ok is false
// when the caller supplied none.
func withLocal(d Decision, l Local, ok bool) Decision {
	switch {
	case !ok:
		d.Kind, d.Reason = Fail, "no local stat for this path"
	case l.Err != nil:
		d.Kind, d.Reason = Fail, "local stat failed: "+l.Err.Error()
	case !l.Exists:
		d.Kind = Copy
	case !l.Regular:
		d.Kind, d.Reason = Fail, "local path exists and is not a regular file"
	case l.Size == d.Size && SameMTime(l.ModTime, d.ModTime):
		d.Kind = Adopt
	default:
		d.Kind, d.Overwrite = Copy, true
	}
	return d
}

// adoptMTimeSlop is how far a local mtime may be from the device's for
// the local file to be adopted. FAT32 stores mtimes to 2 seconds and exFAT
// to 10 ms.
const adoptMTimeSlop = 2 * time.Second

// SameMTime reports whether a local mtime and a device mtime are within
// 2 seconds of each other, close enough to adopt the local file.
func SameMTime(local, device time.Time) bool {
	diff := local.Sub(device)
	return diff >= -adoptMTimeSlop && diff <= adoptMTimeSlop
}

// previous indexes the previous listing's regular files by path. A path
// listed there more than once is left out, so it is not stable yet.
func (in *Input) previous() map[string]Entry {
	if in.FirstPass {
		return nil
	}
	files, _ := uniqueRegular(in.Previous)
	prev := make(map[string]Entry, len(files))
	for _, e := range files {
		prev[e.Path] = e
	}
	return prev
}

// uniqueRegular returns the regular entries whose path appears once in the
// listing, in listing order, and once each the paths that appear more than
// once with at least one regular entry.
func uniqueRegular(listing []Entry) (files []Entry, dups []string) {
	seen := make(map[string]int, len(listing))
	for _, e := range listing {
		seen[e.Path]++
	}
	for _, e := range listing {
		switch {
		case !e.Regular:
		case seen[e.Path] == 1:
			files = append(files, e)
		case seen[e.Path] > 1:
			dups = append(dups, e.Path)
			seen[e.Path] = 0 // report it once
		}
	}
	return files, dups
}
