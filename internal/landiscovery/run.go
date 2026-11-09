package landiscovery

import (
	"context"
	"sync"
)

// Mode is when a connect to a telescope saved with its serial scans this
// network for it.
type Mode string

const (
	// ScanStart scans once per run: the run's first connect follows the
	// telescope (see [Follow]), and later connects reuse the address that
	// connected, scanning again only when a connect there fails.
	ScanStart Mode = "start"
	// ScanConnect scans before every connect.
	ScanConnect Mode = "connect"
	// ScanOff never scans on a connect: it dials the saved address.
	ScanOff Mode = "off"
)

// ParseMode is the mode s names, or false when s names none.
func ParseMode(s string) (Mode, bool) {
	switch m := Mode(s); m {
	case ScanStart, ScanConnect, ScanOff:
		return m, true
	}
	return ScanStart, false
}

// ModeOf is the mode a saved setting s names: [ScanStart] when it is
// empty or names none, so a missing or newer value keeps the default.
func ModeOf(s string) Mode {
	m, _ := ParseMode(s)
	return m
}

// Run is one run's memory of where a scan found a telescope, so that in
// [ScanStart] mode the run scans once. A run is a unit of work the user
// started: one CLI command, one Copy start, one Browse or Free up space
// opening. Its zero value is ready to use, and it is safe for concurrent
// use.
type Run struct {
	mu       sync.Mutex
	searched chan struct{} // closed when the search in progress ends; nil when none is
	key, at  string        // the target last found and where; at is "" when none connected
	epoch    uint64        // the epoch key and at were found in
	newest   uint64        // the newest epoch any connect brought
	gen      uint64        // counts searches, so a failed reuse rescans once
}

type runKey struct{}

// WithRun is ctx carrying r, so the connects made with it share r.
func WithRun(ctx context.Context, r *Run) context.Context {
	return context.WithValue(ctx, runKey{}, r)
}

// RunFrom is the run ctx carries, or nil.
func RunFrom(ctx context.Context) *Run {
	r, _ := ctx.Value(runKey{}).(*Run)
	return r
}

// InRun connects to the target key in run r. The first connect, and any
// after a search failed, calls search, which scans and dials, and keeps
// the address it reports. Later connects dial that address with dial,
// without scanning; when that fails, the connect searches again, once
// for every connect that failed at the same address. A connect that
// arrives while a search is in progress waits for it. InRun returns the
// address it reached and whether it searched.
func InRun[T any](ctx context.Context, r *Run, key string, dial func(context.Context, string) (T, error), search func(context.Context) (T, string, error)) (v T, at string, searched bool, err error) {
	return InRunAt(ctx, r, key, 0, dial, search)
}

// InRunAt is [InRun] in epoch: a connect reuses only an address found in
// its own epoch, and a search that began in an older epoch than the
// newest one a connect brought keeps nothing. Callers number epochs from
// the choices that make an earlier finding untrustworthy, such as an
// explicit save of the telescope, so that a finding made before one is
// never reused after it.
func InRunAt[T any](ctx context.Context, r *Run, key string, epoch uint64, dial func(context.Context, string) (T, error), search func(context.Context) (T, string, error)) (v T, at string, searched bool, err error) {
	var zero T
	for tries := 0; ; tries++ {
		at, gen, err := r.wait(ctx, key, epoch)
		if err != nil {
			return zero, "", false, err
		}
		if at != "" && tries < 2 {
			v, err := dial(ctx, at)
			if err == nil || ctx.Err() != nil {
				return v, at, false, err
			}
			if r.stale(gen) {
				continue // another connect searched since; try where it found
			}
		}
		if !r.begin(key, gen) {
			continue
		}
		v, at, err := search(ctx)
		r.end(key, epoch, at, err == nil)
		return v, at, true, err
	}
}

// wait waits for any search in progress and returns where key was found
// in epoch and the search count.
func (r *Run) wait(ctx context.Context, key string, epoch uint64) (string, uint64, error) {
	for {
		r.mu.Lock()
		r.newest = max(r.newest, epoch)
		ch := r.searched
		if ch == nil {
			at := ""
			if r.key == key && r.epoch == epoch {
				at = r.at
			}
			gen := r.gen
			r.mu.Unlock()
			return at, gen, nil
		}
		r.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return "", 0, ctx.Err()
		}
	}
}

// stale reports that a search began since gen.
func (r *Run) stale(gen uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gen != gen || r.searched != nil
}

// begin starts a search if none began since gen.
func (r *Run) begin(key string, gen uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gen != gen || r.searched != nil {
		return false
	}
	r.gen++
	r.searched = make(chan struct{})
	return true
}

// end ends a search for key in epoch that connected at at, when ok, or
// failed: a failed search keeps no address, so the next connect searches
// again, and neither does one a newer epoch overtook.
func (r *Run) end(key string, epoch uint64, at string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	close(r.searched)
	r.searched = nil
	if epoch < r.newest {
		return
	}
	r.key, r.at, r.epoch = key, "", epoch
	if ok {
		r.at = at
	}
}
