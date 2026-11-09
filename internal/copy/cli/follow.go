package cli

import (
	"context"
	"log/slog"
	"sync"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
)

// followHelp is the help paragraph on following a telescope by its serial.
const followHelp = `An --endpoint filled on this network follows the telescope: when the app
saved it from a scan, each connect scans this network for its serial number
and uses the address that answers with it and accepts the connection, the
saved address first. If the scan doesn't see the telescope, the saved
address is tried; if another telescope answers there instead, it is not
used. A new address is used without saving it. delete keeps the address it
first connected at for the rest of the run.
`

// lanScanner scans this network for a saved telescope, and runs scan.
// Tests replace it with one on fakes.
var lanScanner = &landiscovery.Scanner{}

// lanName is what error text and logs call a telescope saved with its
// serial on this network, in place of the address a scan found it at.
const lanName = "the saved telescope"

// follow is how a run reaches a telescope the app saved on this network
// with its serial, at the endpoint --profile default filled: each connect
// scans for the serial and dials where it answers (see
// [landiscovery.Follow]). It saves nothing: the app follows the telescope
// the same way and saves a new address when it connects there. With pin,
// the first address that connects is the run's from then on, unscanned.
type follow struct {
	saved, serial string
	pin           bool

	mu     sync.Mutex
	pinned string // the address a pinned follow connected at, or ""
	last   string // what was last logged about the search, so a repeat isn't
}

// newFollow is the follow for a run whose --endpoint the profile s filled,
// or nil: a given --endpoint, and an endpoint saved without a serial, are
// dialed as they are.
func newFollow(s Saved, changed func(string) bool) *follow {
	if changed("endpoint") || s.Endpoint == "" || s.LANSerial == "" {
		return nil
	}
	return &follow{saved: s.Endpoint, serial: s.LANSerial}
}

// followDial dials endpoint with d, or, when f is set and endpoint is its
// saved one, follows the telescope and dials with as, whose errors call it
// lanName. The log records the model a scan saw and its reply count,
// never an address, a name or the serial.
func followDial[T any](ctx context.Context, f *follow, endpoint string, log *slog.Logger,
	d func(context.Context, string, *slog.Logger) (T, error), as func(context.Context, string, string, *slog.Logger) (T, error)) (T, error) {
	if f == nil || endpoint != f.saved {
		return d(ctx, endpoint, log)
	}
	dial := func(ctx context.Context, endpoint string) (T, error) { return as(ctx, lanName, endpoint, log) }
	f.mu.Lock()
	pinned := f.pinned
	f.mu.Unlock()
	if pinned != "" {
		return dial(ctx, pinned)
	}
	v, r, err := landiscovery.Follow(ctx, lanScanner, f.saved, f.serial, dial)
	if ctx.Err() != nil {
		return v, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil && f.pin {
		f.pinned = r.Endpoint
	}
	// once logs a search's outcome when it differs from the last one
	// logged, since every connect of a long sync searches.
	once := func(state string, level slog.Level, msg string, keys ...any) {
		if state != f.last {
			f.last = state
			log.Log(ctx, level, msg, keys...)
		}
	}
	switch {
	case err == nil && r.Moved:
		once("moved\x00"+r.Endpoint, slog.LevelInfo, "found the saved telescope at another address on this network", modelKeys(r)...)
	case err == nil && r.NotSeen:
		once("not seen", slog.LevelWarn, "the saved telescope wasn't seen in a scan of this network; tried its saved address")
	case err == nil:
		f.last = ""
	case r.Conflict:
		log.Warn("another telescope answers at the saved address, and a scan of this network didn't find the saved one", "replies", r.Replies)
	case r.Seen != nil:
		log.Warn("found the saved telescope on this network, but it didn't connect where it answered", modelKeys(r)...)
	}
	return v, err
}

func modelKeys(r landiscovery.Resolved) []any {
	keys := []any{"replies", r.Replies}
	if r.Seen != nil && r.Seen.Model != "" {
		keys = append([]any{"model", cloudhttp.ModelLabel(r.Seen.Model)}, keys...)
	}
	return keys
}
