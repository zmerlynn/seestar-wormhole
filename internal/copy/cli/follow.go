package cli

import (
	"context"
	"log/slog"
	"sync"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
)

// followHelp is the help paragraph on following a moved telescope.
const followHelp = `An --endpoint filled on this network follows the telescope: when the app
saved it from a scan and it doesn't answer at the saved address, a scan of
this network finds it by its serial number, and the run uses the new
address without saving it.
`

// lanScanner scans this network for a saved telescope that moved, and runs
// scan. Tests replace it with one on fakes.
var lanScanner = &landiscovery.Scanner{}

// follow is how a run reaches a telescope the app saved on this network
// with its serial, at the endpoint --profile default filled: at the saved
// address first, and when that gets no answer, wherever a scan of this
// network finds the serial. The run keeps using the address it found. It
// saves nothing: the app follows the telescope the same way and saves the
// new address the next time it connects.
type follow struct {
	saved, serial string
	mu            sync.Mutex
	at            string
}

// newFollow is the follow for a run whose --endpoint the profile s filled,
// or nil: a given --endpoint, and an endpoint saved without a serial, are
// dialed as they are.
func newFollow(s Saved, changed func(string) bool) *follow {
	if changed("endpoint") || s.Endpoint == "" || s.LANSerial == "" {
		return nil
	}
	return &follow{saved: s.Endpoint, serial: s.LANSerial, at: s.Endpoint}
}

// followDial dials endpoint with d, following the telescope when f is set
// and endpoint is its saved one. The log records the model a scan saw and
// its reply count, never an address, a name or the serial.
func followDial[T any](ctx context.Context, f *follow, endpoint string, log *slog.Logger, d func(context.Context, string, *slog.Logger) (T, error)) (T, error) {
	if f == nil || endpoint != f.saved {
		return d(ctx, endpoint, log)
	}
	f.mu.Lock()
	at := f.at
	f.mu.Unlock()
	v, r, err := landiscovery.Redial(ctx, lanScanner, at, f.serial, func(ctx context.Context, endpoint string) (T, error) {
		return d(ctx, endpoint, log)
	})
	switch {
	case ctx.Err() != nil:
	case r.Moved:
		f.mu.Lock()
		f.at = r.Endpoint
		f.mu.Unlock()
		log.Info("found the saved telescope at a new address on this network", modelKeys(r)...)
	case r.Seen != nil:
		log.Warn("found the saved telescope on this network, but it didn't answer at its new address", modelKeys(r)...)
	case err != nil && landiscovery.Unreached(err):
		log.Warn("the saved telescope didn't answer, and a scan of this network didn't find it", "replies", r.Replies)
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
