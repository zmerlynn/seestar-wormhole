package landiscovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"sync"
)

// ErrOtherTelescope is a connect to a telescope saved with its serial
// whose saved address advertised another serial in the scan, so that
// address wasn't used, and no other address connected. It stays true when
// the saved serial also answered, from that address or elsewhere.
var ErrOtherTelescope = errors.New("the saved address advertised a different telescope and cannot be used for this target")

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
// connects. An address whose dial fails doesn't end the search.
//
// Once another serial has answered from the saved address, that address
// is never used for this telescope: a connection to it made while that
// reply arrived is released and the search goes on, within the same scan.
// A connection to another address is kept over the saved one only if the
// saved address hasn't answered with serial by the time it connects;
// otherwise the saved address is dialed too, and whichever connection
// loses is released. release owns every connection Follow doesn't return.
//
// When the scan found serial nowhere it dials endpoint as it is, unless
// another serial answered from the saved address: then it fails with
// [ErrOtherTelescope]. When serial answered but no address connected, it
// returns the last dial's error, or ErrOtherTelescope when the only
// address was the disqualified saved one. A nil error always comes with a
// connection from dial. Without a serial or a scanner it dials endpoint
// alone, without scanning.
func Follow[T any](ctx context.Context, s *Scanner, endpoint, serial string, dial func(context.Context, string) (T, error), release func(T)) (T, Resolved, error) {
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
	// collect moves what the scan has found so far into pending.
	collect := func() {
		for {
			select {
			case a := <-w.found:
				pending = append(pending, a)
			default:
				return
			}
		}
	}
	// takeSaved removes the saved address from pending, and reports
	// whether it was there and may be used.
	takeSaved := func() bool {
		i := slices.Index(pending, saved)
		if i < 0 {
			return false
		}
		pending = slices.Delete(pending, i, i+1)
		return !w.conflicted()
	}
	// done ends a successful search at address.
	done := func(v T, address netip.Addr) (T, Resolved, error) {
		stop()
		r := w.resolved(endpoint, result.Replies)
		r.Endpoint = net.JoinHostPort(address.String(), port)
		r.Moved = address != saved
		return v, r, nil
	}
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
				return zero, w.resolved(endpoint, result.Replies), ctx.Err()
			}
		}
		collect()
		if len(pending) == 0 {
			if scanning {
				continue
			}
			break
		}
		var address netip.Addr
		if slices.Contains(pending, saved) {
			if !takeSaved() {
				continue
			}
			address = saved
		} else {
			address, pending = pending[0], pending[1:]
		}
		v, err := dial(ctx, net.JoinHostPort(address.String(), port))
		if err != nil {
			last = err
			if ctx.Err() != nil {
				stop()
				return zero, w.resolved(endpoint, result.Replies), err
			}
			continue
		}
		if address == saved {
			// Another serial may have answered from it during the dial.
			if w.conflicted() {
				release(v)
				continue
			}
			return done(v, saved)
		}
		// The saved address may have answered with serial during this
		// dial; it is still preferred.
		collect()
		if !takeSaved() {
			return done(v, address)
		}
		u, err := dial(ctx, net.JoinHostPort(saved.String(), port))
		switch {
		case err != nil:
			if ctx.Err() != nil {
				release(v)
				stop()
				return zero, w.resolved(endpoint, result.Replies), err
			}
			return done(v, address)
		case w.conflicted():
			release(u)
			return done(v, address)
		}
		release(v)
		return done(u, saved)
	}
	r := w.resolved(endpoint, result.Replies)
	switch {
	case ctx.Err() != nil:
		return zero, r, ctx.Err()
	case last != nil:
		return zero, r, last
	case r.Conflict:
		return zero, r, ErrOtherTelescope
	case r.Seen != nil:
		// Every address that answered with serial is dialed or, being the
		// saved one, disqualified, so this isn't reached; a nil error
		// without a connection must not be.
		return zero, r, errors.New("the saved telescope answered, but no address was dialed")
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

// resolved is a Resolved at endpoint with what the scan saw so far.
func (w *watcher) resolved(endpoint string, replies int) Resolved {
	w.mu.Lock()
	defer w.mu.Unlock()
	return Resolved{Endpoint: endpoint, Seen: w.seen, Conflict: w.conflict, Replies: replies}
}

func (w *watcher) conflicted() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conflict
}
