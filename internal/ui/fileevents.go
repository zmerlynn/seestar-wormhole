package ui

import (
	"cmp"
	"slices"
	"time"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// opKey is one file-tool operation: a source and an op.
type opKey struct{ source, op string }

// opMessages are an operation's fixed lines: its failure, its failure with
// only failed files when it has one, and its recovery.
type opMessages struct{ failed, failedFiles, recovered string }

// fileMessages is every file-tool operation the trail records. An outcome
// of any other (source, op) is dropped, as eventCode drops unknown text.
var fileMessages = map[opKey]opMessages{
	{diagnostic.SourceCopy, diagnostic.OpStart}:          {"Copying couldn't start.", "", "Copying started."},
	{diagnostic.SourceCopy, diagnostic.OpOpen}:           {"Copying stopped: it couldn't open its folder or history.", "", "Copying opened its folder and history again."},
	{diagnostic.SourceCopy, diagnostic.OpOtherTelescope}: {"Copying stopped: the connection reaches another telescope.", "", "Copying is running again."},
	{diagnostic.SourceCopy, diagnostic.OpPass}:           {"Copying failed.", "Copying finished with failed files.", "Copying finished a pass without failures."},
	{diagnostic.SourceCopy, diagnostic.OpPreview}:        {"Copy preview failed.", "", "Copy preview worked again."},
	{diagnostic.SourceCopy, diagnostic.OpTest}:           {"Test couldn't reach the telescope.", "", "Test reached the telescope again."},
	{diagnostic.SourceCopy, diagnostic.OpSaveSettings}:   {"Copy settings couldn't be saved.", "", "Copy settings were saved."},
	{diagnostic.SourceCopy, diagnostic.OpClearHistory}:   {"Copy history couldn't be cleared.", "", "Copy history was cleared."},
	{diagnostic.SourceCopy, diagnostic.OpChooseFolder}:   {"The folder picker failed.", "", "The folder picker worked again."},
	{diagnostic.SourceBrowse, diagnostic.OpConnect}:      {"Browse couldn't reach the telescope.", "", "Browse reached the telescope again."},
	{diagnostic.SourceBrowse, diagnostic.OpRecords}:      {"Browse couldn't read the copy history.", "", "Browse read the copy history again."},
	{diagnostic.SourceBrowse, diagnostic.OpRead}:         {"Browse couldn't read from the telescope.", "", "Browse read from the telescope again."},
	{diagnostic.SourceBrowse, diagnostic.OpSaveFile}:     {"Browse couldn't save a file.", "", "Browse saved a file again."},
	{diagnostic.SourceDelete, diagnostic.OpPreview}:      {"Free up space preview failed.", "", "Free up space preview worked again."},
	{diagnostic.SourceDelete, diagnostic.OpDelete}:       {"Free up space failed.", "Free up space finished with failed files.", "Free up space finished without failures."},
	{diagnostic.SourceDelete, diagnostic.OpSaveOptions}:  {"Free up space options couldn't be saved.", "", "Free up space options were saved."},
}

// failState is a failing operation: its current failure class, its last
// count of failed files, how many failures it has had in that class, and
// when the class began and last failed.
type failState struct {
	phase, reason string
	count         int
	failures      int
	since, last   time.Time
}

// opFailure is a failing operation as /api/status and the summary show it.
type opFailure struct {
	Source   string    `json:"source"`
	Op       string    `json:"op"`
	Code     string    `json:"code"`
	Phase    string    `json:"phase"`
	Reason   string    `json:"reason"`
	Count    int       `json:"count"`
	Failures int       `json:"failures"`
	Since    time.Time `json:"since"`
	Last     time.Time `json:"last"`
}

// addOutcome records a file-tool outcome. It writes a line on a key's
// first failure, on a change of its failure class and on its recovery,
// and keeps everything else in memory. An outcome no newer than its key's
// last one is stale and dropped, so an outcome reported late can't undo a
// later one; a canceled one changes nothing. It must not be called with a
// Copy, Browse or Free up space lock held.
func (l *eventLog) addOutcome(o copyapp.Outcome) {
	if l == nil || o.Canceled {
		return
	}
	key := opKey{o.Source, o.Op}
	msgs, ok := fileMessages[key]
	if !ok {
		return
	}
	l.diskMu.Lock()
	defer l.diskMu.Unlock()
	l.mu.Lock()
	entry := l.outcomeLocked(key, msgs, o)
	l.mu.Unlock()
	l.write(entry)
}

// outcomeLocked applies o to its key's state in memory and returns the line
// to write, if any. The caller holds mu.
func (l *eventLog) outcomeLocked(key opKey, msgs opMessages, o copyapp.Outcome) *event {
	if o.Seq <= l.lastSeq[key] {
		return nil
	}
	if l.lastSeq == nil {
		l.lastSeq = map[opKey]uint64{}
		l.failing = map[opKey]*failState{}
	}
	l.lastSeq[key] = o.Seq
	now := time.Now().UTC()
	entry := event{Time: now, Source: key.source, Op: key.op}
	state := l.failing[key]
	if !o.Failed() {
		if state == nil {
			return nil
		}
		delete(l.failing, key)
		entry.Message, entry.Code = msgs.recovered, diagnostic.Code(key.source, key.op, false)
		l.appendLocked(entry)
		return &entry
	}
	phase, reason := diagnostic.OpFields(key.op, o.Err)
	if state != nil && state.phase == phase && state.reason == reason {
		state.count, state.last = o.Count, now
		state.failures++
		return nil
	}
	l.failing[key] = &failState{phase: phase, reason: reason, count: o.Count, failures: 1, since: now, last: now}
	entry.Message = msgs.failed
	if o.Err == nil && msgs.failedFiles != "" {
		entry.Message = msgs.failedFiles
	}
	entry.Code, entry.Phase, entry.Reason, entry.Count = diagnostic.Code(key.source, key.op, true), phase, reason, o.Count
	l.appendLocked(entry)
	return &entry
}

// failuresLocked returns the failing operations, oldest class first. The
// caller holds mu.
func (l *eventLog) failuresLocked() []opFailure {
	list := []opFailure{}
	for key, s := range l.failing {
		list = append(list, opFailure{Source: key.source, Op: key.op, Code: diagnostic.Code(key.source, key.op, true),
			Phase: s.phase, Reason: s.reason, Count: s.count, Failures: s.failures, Since: s.since, Last: s.last})
	}
	slices.SortFunc(list, func(a, b opFailure) int {
		return cmp.Or(a.Since.Compare(b.Since), cmp.Compare(a.Source, b.Source), cmp.Compare(a.Op, b.Op))
	})
	return list
}
