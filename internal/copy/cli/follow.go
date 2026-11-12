package cli

import (
	"context"
	"io"
	"log/slog"
	"sync"

	"github.com/spf13/pflag"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
)

// followHelp is the help paragraph on following a telescope by its serial.
const followHelp = `An --endpoint filled on this network follows the telescope when the app
saved it from a scan: a scan of this network looks for its serial number and
uses the address that answers with it and accepts the connection, the saved
address first. If the scan doesn't see the telescope, the saved address is
tried; if another telescope answers there instead, it is not used. A new
address is used without saving it. --scan says when to scan, overriding the
app's setting: start (the default) scans once per run, at its first connect,
and again only when a connect fails; connect scans before every connect; off
never scans and dials the saved address. A given --endpoint, or one saved
without a serial, is dialed as it is, whatever --scan says. delete keeps the
address it first connected at for the rest of the run.
`

// lanScanner scans this network for a saved telescope, and runs scan.
// Tests replace it with one on fakes.
var lanScanner = &landiscovery.Scanner{}

// follow is how a run reaches a telescope the app saved on this network
// with its serial, at the endpoint --profile default filled: a connect
// scans for the serial and dials where it answers (see
// [landiscovery.Follow]), when mode says to. It saves nothing: the app
// follows the telescope the same way and saves a new address when it
// connects there. With pin, the first address that connects is the run's
// from then on, unscanned.
type follow struct {
	saved, serial string
	mode          landiscovery.Mode
	pin           bool
	run           landiscovery.Run // the run's scan, in start mode

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
	return &follow{saved: s.Endpoint, serial: s.LANSerial, mode: landiscovery.ModeOf(s.LANScan)}
}

// scanFlag is --scan, which overrides when a follow scans.
type scanFlag struct{ value string }

func (s *scanFlag) register(f *pflag.FlagSet) {
	f.StringVar(&s.value, "scan", "", "when to scan this network for the telescope --profile default saved there: start, connect or off (default: the app's setting, start if none)")
}

// apply checks --scan when given and sets f's mode to it. It is checked
// without a follow too, though nothing is scanned then.
func (s *scanFlag) apply(flags *pflag.FlagSet, f *follow) error {
	if !flags.Changed("scan") {
		return nil
	}
	m, ok := landiscovery.ParseMode(s.value)
	if !ok {
		return &diagnostic.OptionError{Option: "--scan", Rule: "use start, connect or off"}
	}
	if f != nil {
		f.mode = m
	}
	return nil
}

// followDial dials endpoint with d, or, when f is set and endpoint is its
// saved one, reaches the telescope as f's mode says: off dials the saved
// address; connect follows the telescope; start follows it once per run
// and dials where it connected after that, following it again when that
// fails. The log names the telescope a scan saw, the addresses and the
// reply count.
func followDial[T io.Closer](ctx context.Context, f *follow, endpoint string, log *slog.Logger,
	d func(context.Context, string, *slog.Logger) (T, error)) (T, error) {
	if f == nil || endpoint != f.saved {
		return d(ctx, endpoint, log)
	}
	dial := func(ctx context.Context, endpoint string) (T, error) { return d(ctx, endpoint, log) }
	f.mu.Lock()
	pinned := f.pinned
	f.mu.Unlock()
	switch {
	case pinned != "":
		return dial(ctx, pinned)
	case f.mode == landiscovery.ScanOff:
		return dial(ctx, f.saved)
	case f.mode == landiscovery.ScanConnect:
		v, _, err := followSearch(ctx, f, log, dial)
		return v, err
	}
	v, _, _, err := landiscovery.InRun(ctx, &f.run, f.saved+"\x00"+f.serial, dial,
		func(ctx context.Context) (T, string, error) { return followSearch(ctx, f, log, dial) })
	return v, err
}

// followSearch is one search for f's telescope, which scans, and what it
// logs. It returns the address it connected at.
func followSearch[T io.Closer](ctx context.Context, f *follow, log *slog.Logger, dial func(context.Context, string) (T, error)) (T, string, error) {
	v, r, err := landiscovery.Follow(ctx, lanScanner, f.saved, f.serial, dial, func(v T) { v.Close() })
	if ctx.Err() != nil {
		return v, r.Endpoint, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil && f.pin {
		f.pinned = r.Endpoint
	}
	// once logs a search's outcome when it differs from the last one
	// logged, since every connect of a long sync may search.
	once := func(state string, level slog.Level, msg string, keys ...any) {
		if state != f.last {
			f.last = state
			log.Log(ctx, level, msg, keys...)
		}
	}
	switch {
	case err == nil && r.Moved:
		once("moved\x00"+r.Endpoint, slog.LevelInfo, "found the telescope at another address on this network", telescopeKeys(f, r)...)
	case r.NotSeen:
		once("not seen", slog.LevelWarn, "the telescope wasn't seen in a scan of this network; tried its saved address", telescopeKeys(f, r)...)
	case err == nil:
		f.last = ""
	case r.Conflict && r.Seen == nil:
		log.Warn("another telescope answers at the saved address, and a scan of this network didn't find the saved one", telescopeKeys(f, r)...)
	case r.Seen != nil:
		log.Warn("found the telescope on this network, but it didn't connect where it answered", telescopeKeys(f, r)...)
	}
	return v, r.Endpoint, err
}

// telescopeKeys name f's telescope, as the scan saw it or by its serial,
// the saved address, the endpoint r reached, the addresses where the scan
// saw it, the telescope that answered at the saved address instead, and
// the scan's reply count.
func telescopeKeys(f *follow, r landiscovery.Resolved) []any {
	keys := []any{"serial", f.serial}
	if r.Seen != nil {
		keys = []any{"telescope", r.Seen.Label()}
	}
	keys = append(keys, "saved", f.saved)
	if r.Endpoint != f.saved {
		keys = append(keys, "endpoint", r.Endpoint)
	}
	if r.Seen != nil {
		keys = append(keys, "addresses", r.Seen.Addresses)
	}
	if r.Other != nil {
		keys = append(keys, "other", r.Other.Label())
	}
	return append(keys, "replies", r.Replies)
}
