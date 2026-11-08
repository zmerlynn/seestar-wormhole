package app

import (
	"errors"
	"sync/atomic"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/engine"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// Outcome is how one Copy, Browse or Free up space operation ended, for the
// host's diagnostic trail. Source and Op are the constants in
// internal/diagnostic; Err is read only through diagnostic's projections,
// never formatted. A failure has Err set or a positive Count of failed
// files; a success has neither. A canceled operation was stopped, quit,
// discarded or made stale, lost its request or had its dialog dismissed:
// it neither fails nor clears. Seq orders the outcomes of the process: it
// is drawn where the operation ends, under the lock that orders its
// completions, so a later Seq ended later.
type Outcome struct {
	Source, Op string
	Err        error
	Count      int
	Canceled   bool
	Seq        uint64
}

// Failed reports whether o is a failure.
func (o Outcome) Failed() bool { return !o.Canceled && (o.Err != nil || o.Count > 0) }

// reporter hands outcomes to the host's Report, numbered from the Mount's
// one sequence. A nil reporter, or one without Report, discards them.
type reporter struct {
	report func(Outcome)
	seq    atomic.Uint64
}

// next is the next sequence number. The caller holds the lock that orders
// the completions of the operation it numbers.
func (r *reporter) next() uint64 {
	if r == nil {
		return 0
	}
	return r.seq.Add(1)
}

// send reports each outcome in order. The caller holds no Copy, Browse or
// Free up space lock, s.admit included.
func (r *reporter) send(outcomes ...Outcome) {
	if r == nil || r.report == nil {
		return
	}
	for _, o := range outcomes {
		r.report(o)
	}
}

// classified is err for the trail: a history or folder lock held elsewhere
// reads as busy.
func classified(err error) error {
	if errors.Is(err, state.ErrLocked) || errors.Is(err, state.ErrFolderLocked) {
		return diagnostic.Wrap("open", "busy", err)
	}
	return err
}

// passOutcomes are a pass's: its own, and the start, open and other
// telescope operations that any pass clears, all numbered seq.
func passOutcomes(sum engine.Summary, seq uint64) []Outcome {
	return []Outcome{
		{Source: diagnostic.SourceCopy, Op: diagnostic.OpPass, Err: classified(sum.Err), Count: len(sum.Failed), Seq: seq},
		{Source: diagnostic.SourceCopy, Op: diagnostic.OpStart, Seq: seq},
		{Source: diagnostic.SourceCopy, Op: diagnostic.OpOpen, Seq: seq},
		{Source: diagnostic.SourceCopy, Op: diagnostic.OpOtherTelescope, Seq: seq},
	}
}
