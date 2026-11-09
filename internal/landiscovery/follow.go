package landiscovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"sync"
)

// ErrOtherTelescope is a connect to a telescope saved with its serial that
// found another telescope answering at the saved address and the saved one
// nowhere. The saved address is not used.
var ErrOtherTelescope = errors.New("another telescope answers at the saved address, and the saved telescope wasn't found on this network")

// Resolved is how [Follow] reached a telescope. Endpoint is the endpoint it
// connected to, or the saved one when none connected; Moved reports that it
// is not the saved address. Seen is the telescope as the scan saw it when
// the serial answered, so its model can be logged. NotSeen reports that the
// scan found the serial nowhere and the saved address was dialed without
// that evidence. Conflict reports that another serial answered from the
// saved address. Replies is the scan's reply count.
type Resolved struct {
	Endpoint string
	Moved    bool
	Seen     *Telescope
	NotSeen  bool
	Conflict bool
	Replies  int
}

// Follow connects to a telescope saved as endpoint (host:port) with serial.
// Every connect scans: it dials each address that answers the scan with
// serial, on endpoint's port, as the replies arrive, the saved address
// first when it is among those waiting, and stops the scan once one
// connects. An address whose dial fails doesn't end the search. When the
// scan found serial nowhere it dials endpoint as it is, unless another
// serial answered from the saved address: that address is never used for
// this telescope. When serial answered but no address connected, it
// returns the last dial's error. Without a serial or a scanner it dials
// endpoint alone, without scanning.
func Follow[T any](ctx context.Context, s *Scanner, endpoint, serial string, dial func(context.Context, string) (T, error)) (T, Resolved, error) {
	var zero T
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || serial == "" || s == nil {
		v, err := dial(ctx, endpoint)
		return v, Resolved{Endpoint: endpoint}, err
	}
	saved, _ := netip.ParseAddr(host) // invalid for a name
	saved = saved.Unmap()
	w := &watcher{serial: serial, saved: saved, found: make(chan netip.Addr, maxAddresses)}
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	finished := make(chan struct{})
	var result ScanResult
	go func() {
		defer close(finished)
		result, _ = s.watch(scanCtx, w.observe)
	}()
	// stop ends the scan and waits for it, so its socket is closed and
	// result is set before Follow returns.
	stop := func() {
		cancel()
		<-finished
	}
	var pending []netip.Addr
	var last error
	scanning := true
	for {
		if len(pending) == 0 && scanning {
			select {
			case a := <-w.found:
				pending = append(pending, a)
			case <-finished:
				scanning = false
			case <-ctx.Done():
				stop()
				return zero, Resolved{Endpoint: endpoint, Replies: result.Replies}, ctx.Err()
			}
		}
	drain:
		for {
			select {
			case a := <-w.found:
				pending = append(pending, a)
			default:
				break drain
			}
		}
		if len(pending) == 0 {
			if scanning {
				continue
			}
			break
		}
		i := max(slices.Index(pending, saved), 0)
		address := pending[i]
		pending = slices.Delete(pending, i, i+1)
		if address == saved && w.conflicted() {
			continue
		}
		next := net.JoinHostPort(address.String(), port)
		v, err := dial(ctx, next)
		if err == nil {
			stop()
			return v, Resolved{Endpoint: next, Moved: address != saved, Seen: w.telescope(), Replies: result.Replies}, nil
		}
		last = err
		if ctx.Err() != nil {
			stop()
			return zero, Resolved{Endpoint: endpoint, Seen: w.telescope(), Replies: result.Replies}, err
		}
	}
	r := Resolved{Endpoint: endpoint, Seen: w.telescope(), Conflict: w.conflicted(), Replies: result.Replies}
	switch {
	case ctx.Err() != nil:
		return zero, r, ctx.Err()
	case r.Seen != nil:
		return zero, r, last
	case r.Conflict:
		return zero, r, ErrOtherTelescope
	}
	r.NotSeen = true
	v, err := dial(ctx, endpoint)
	return v, r, err
}

// watcher collects a scan's replies for one serial as they arrive: found
// carries each new address that answered with it, at most maxAddresses,
// so a send never blocks.
type watcher struct {
	serial string
	saved  netip.Addr
	found  chan netip.Addr

	mu       sync.Mutex
	seen     *Telescope
	sent     []netip.Addr
	conflict bool
}

func (w *watcher) observe(t Telescope, from netip.Addr) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t.Serial != w.serial {
		if from == w.saved {
			w.conflict = true
		}
		return
	}
	if w.seen == nil {
		t.Addresses = nil
		w.seen = &t
	}
	if slices.Contains(w.sent, from) || len(w.sent) >= maxAddresses {
		return
	}
	w.sent = append(w.sent, from)
	w.found <- from
}

func (w *watcher) telescope() *Telescope {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seen
}

func (w *watcher) conflicted() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conflict
}
