package app

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
