package landiscovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync"
)

// SMBPort is the telescope's file-sharing port, and ProxySMBPort the port
// a seestar-wormhole proxy forwards it on by default. A proxy with
// discovery on answers scans for the telescope, so an address that
// answers with a serial may be either.
const (
	SMBPort      = "445"
	ProxySMBPort = "4445"
)

// OtherSMBPort is the other of SMBPort and ProxySMBPort, when port is one
// of them.
func OtherSMBPort(port string) (string, bool) {
	switch port {
	case SMBPort:
		return ProxySMBPort, true
	case ProxySMBPort:
		return SMBPort, true
	}
	return "", false
}

// ErrOtherTelescope is a connect to a telescope saved with its serial
// whose saved address advertised another serial in the scan, so that
// address wasn't used, and no other address connected. It stays true when
// the saved serial also answered, from that address or elsewhere.
var ErrOtherTelescope = errors.New("the saved address advertised a different telescope and cannot be used for this target")

// Resolved is how [Follow] reached a telescope. Endpoint is the endpoint it
// connected to, or the saved one when none connected; Moved reports that it
// is not the saved address and port. Seen is the telescope as the scan saw it when
// the serial answered, so it can be named, with the addresses it answered
// from. NotSeen reports that the scan
// found the serial nowhere and the saved address was dialed without that
// evidence. Conflict reports that another serial answered from the saved
// address, and Other is the first telescope that did. Replies is the
// scan's reply count.
type Resolved struct {
	Endpoint string
	Moved    bool
	Seen     *Telescope
	NotSeen  bool
	Conflict bool
	Other    *Telescope
	Replies  int
}

// Follow connects to a telescope saved as endpoint (host:port) with serial.
// Every connect scans: it dials each address that answers the scan with
// serial, on endpoint's port, as the replies arrive, the saved address
// first when it is among those waiting, and stops the scan once one
// connects. When endpoint's port is SMBPort or ProxySMBPort and the dial
// fails, it dials the other of the two at the same address next, within
// the same ctx. An address whose dials fail doesn't end the search.
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
	// dialAt dials address on endpoint's port, then on the other SMB
	// port, and returns the port that connected. A failure is the first
	// dial's.
	dialAt := func(address netip.Addr) (T, string, error) {
		v, err := dial(ctx, net.JoinHostPort(address.String(), port))
		other, ok := OtherSMBPort(port)
		if err == nil || !ok || ctx.Err() != nil {
			return v, port, err
		}
		if v, otherErr := dial(ctx, net.JoinHostPort(address.String(), other)); otherErr == nil {
			return v, other, nil
		}
		return zero, port, err
	}
	// done ends a successful search at address, on port at.
	done := func(v T, address netip.Addr, at string) (T, Resolved, error) {
		stop()
		r := w.resolved(endpoint, result.Replies)
		r.Endpoint = net.JoinHostPort(address.String(), at)
		r.Moved = address != saved || at != port
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
		v, at, err := dialAt(address)
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
			return done(v, saved, at)
		}
		// The saved address may have answered with serial during this
		// dial; it is still preferred.
		collect()
		if !takeSaved() {
			return done(v, address, at)
		}
		u, savedAt, err := dialAt(saved)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				release(v)
				stop()
				return zero, w.resolved(endpoint, result.Replies), err
			}
			return done(v, address, at)
		case w.conflicted():
			release(u)
			return done(v, address, at)
		}
		release(v)
		return done(u, saved, savedAt)
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
		return zero, r, fmt.Errorf("serial %s answered, but no address was dialed", escaped(serial))
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
	other    *Telescope
}

func (w *watcher) observe(t Telescope, from netip.Addr) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t.Serial != w.serial {
		if from == w.saved && !w.conflict {
			w.conflict = true
			t.Addresses = []netip.Addr{from}
			w.other = &t
		}
		return
	}
	if w.seen == nil {
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
	r := Resolved{Endpoint: endpoint, Conflict: w.conflict, Other: w.other, Replies: replies}
	if w.seen != nil {
		seen := *w.seen
		seen.Addresses = slices.Clone(w.sent)
		r.Seen = &seen
	}
	return r
}

func (w *watcher) conflicted() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conflict
}
