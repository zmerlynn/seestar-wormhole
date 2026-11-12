package landiscovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"sync"
	"time"
)

// SMBPort is the telescope's file-sharing port, and ProxySMBPort the port
// a seestar-wormhole proxy forwards it on by default. A proxy with
// discovery on answers scans for the telescope, so an address that
// answers with a serial may be either.
const (
	SMBPort      = "445"
	ProxySMBPort = "4445"
)

// smbPorts is the ports to dial, in order, at an address that answered
// with the serial of a telescope saved on port: SMBPort then ProxySMBPort
// when port is either, or else port alone.
func smbPorts(port string) []string {
	if port == SMBPort || port == ProxySMBPort {
		return []string{SMBPort, ProxySMBPort}
	}
	return []string{port}
}

// PortsError is a failure to connect to one address on two ports:
// Errs[i] is the failure at Endpoints[i], tried in that order. It names
// both, and unwraps to the one that got further (see [PortsError.Endpoint]).
type PortsError struct {
	Endpoints [2]string
	Errs      [2]error
}

func (e *PortsError) Error() string {
	return e.Endpoints[0] + ": " + e.Errs[0].Error() + "; " + e.Endpoints[1] + ": " + e.Errs[1].Error()
}

func (e *PortsError) Unwrap() error { return e.Errs[e.further()] }

// Endpoint is the endpoint whose failure got further: the second when it
// was canceled or ran out of time, so the connect's end is reported, or
// when the first didn't connect and the second did before it failed;
// otherwise the first.
func (e *PortsError) Endpoint() string { return e.Endpoints[e.further()] }

func (e *PortsError) further() int {
	if errors.Is(e.Errs[1], context.Canceled) || errors.Is(e.Errs[1], context.DeadlineExceeded) ||
		unconnected(e.Errs[0]) && !unconnected(e.Errs[1]) {
		return 1
	}
	return 0
}

// unconnected reports whether err is a failure to open a connection.
func unconnected(err error) bool {
	op, ok := errors.AsType[*net.OpError](err)
	return ok && op.Op == "dial"
}

// ErrOtherTelescope is a connect to a telescope saved with its serial
// where every address it could use advertised another serial in the scan,
// so none was used: the saved address, when the serial wasn't seen
// elsewhere, or each address it answered from. It stays true when the
// saved serial also answered from an address it disqualified.
var ErrOtherTelescope = errors.New("the address advertised a different telescope and cannot be used for this target")

// maxOthers bounds the addresses a [Follow] remembers another serial
// answered from. Once more have, it uses none: it can't tell which.
const maxOthers = 256

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
// serial as the replies arrive, the saved address first when it is among
// those waiting, and stops the scan once one connects. When endpoint's
// port is SMBPort or ProxySMBPort, it dials SMBPort at each address, then
// ProxySMBPort when that fails, within the same ctx; any other port is
// dialed alone. An address whose dials fail doesn't end the search.
//
// Once another serial has answered from an address, that address is never
// used for this telescope, the saved one or any other: it isn't dialed, or
// dialed on its next port, and a connection to it made while that reply
// arrived, up to the scan's end, is released and the search goes on. When
// more than maxOthers addresses have answered with other serials, no
// address is used. A connection to another address is kept over the saved
// one only if the saved address hasn't answered with serial by the time it
// connects; otherwise the saved address is dialed too, and whichever
// connection loses is released. release owns every connection Follow
// doesn't return.
//
// When the scan found serial nowhere it dials endpoint as it is, unless
// another serial answered from the saved address: then it fails with
// [ErrOtherTelescope]. When serial answered but no address connected, it
// returns the last dial's error, or ErrOtherTelescope when no address it
// answered from could be used. A nil error always comes with a connection
// from dial. Without a serial or a scanner it dials endpoint alone, without
// scanning.
//
// A connect that scans, from the scan's start to the last dial's end, is
// bounded by ConnectTimeout, and fails with an error that unwraps to
// [context.DeadlineExceeded] when it runs out; ctx bounds it too. The
// budget ends when the connect does: a returned connection's context
// is canceled only with ctx.
func Follow[T any](ctx context.Context, s *Scanner, endpoint, serial string, dial func(context.Context, string) (T, error), release func(T)) (T, Resolved, error) {
	var zero T
	if _, _, err := net.SplitHostPort(endpoint); err != nil || serial == "" || s == nil {
		v, err := dial(ctx, endpoint)
		return v, Resolved{Endpoint: endpoint}, err
	}
	connectCtx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(ConnectTimeout, cancel)
	v, r, err := follow(connectCtx, s, endpoint, serial, dial, release)
	if timer.Stop() {
		if err != nil {
			cancel()
		}
		return v, r, err
	}
	// The budget ran out, even if a connection was made as it did.
	if err == nil {
		release(v)
		r.Endpoint, r.Moved = endpoint, false
	}
	if ctx.Err() != nil {
		return zero, r, ctx.Err()
	}
	return zero, r, errNoAnswer{}
}

// ConnectTimeout bounds a [Follow] that scans.
const ConnectTimeout = 30 * time.Second

// errNoAnswer is a Follow that ran out of ConnectTimeout. It classifies as
// a timeout.
type errNoAnswer struct{}

func (errNoAnswer) Error() string {
	return "connect to the telescope: no answer after " + ConnectTimeout.String()
}

func (errNoAnswer) Unwrap() error { return context.DeadlineExceeded }

// follow is Follow's connect that scans, within ctx.
func follow[T any](ctx context.Context, s *Scanner, endpoint, serial string, dial func(context.Context, string) (T, error), release func(T)) (T, Resolved, error) {
	var zero T
	host, port, _ := net.SplitHostPort(endpoint)
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
	// dialAt dials address on each of endpoint's ports in turn (see
	// [smbPorts]), and returns the port that connected. When both fail,
	// the failure is a [PortsError]. An address another serial answered
	// from during the first dial isn't dialed again.
	ports := smbPorts(port)
	dialAt := func(address netip.Addr) (T, string, error) {
		first := net.JoinHostPort(address.String(), ports[0])
		v, err := dial(ctx, first)
		if err == nil || len(ports) == 1 || ctx.Err() != nil || w.disqualified(address) {
			return v, ports[0], err
		}
		second := net.JoinHostPort(address.String(), ports[1])
		v, otherErr := dial(ctx, second)
		if otherErr == nil {
			return v, ports[1], nil
		}
		return zero, ports[0], &PortsError{[2]string{first, second}, [2]error{err, otherErr}}
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
		address := pending[0]
		if i := slices.Index(pending, saved); i >= 0 {
			address = saved
			pending = slices.Delete(pending, i, i+1)
		} else {
			pending = pending[1:]
		}
		if w.disqualified(address) {
			continue
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
		if w.disqualified(address) {
			release(v)
			continue
		}
		// The saved address may have answered with serial during this
		// dial; it is still preferred.
		collect()
		if i := slices.Index(pending, saved); address != saved && i >= 0 && !w.disqualified(saved) {
			pending = slices.Delete(pending, i, i+1)
			u, savedAt, err := dialAt(saved)
			switch {
			case err != nil && ctx.Err() != nil:
				release(v)
				stop()
				return zero, w.resolved(endpoint, result.Replies), err
			case err != nil:
			case w.disqualified(saved):
				release(u)
			default:
				release(v)
				v, address, at = u, saved, savedAt
			}
		}
		// The scan's last replies may still disqualify the address.
		stop()
		if w.disqualified(address) {
			release(v)
			continue
		}
		r := w.resolved(endpoint, result.Replies)
		r.Endpoint = net.JoinHostPort(address.String(), at)
		r.Moved = address != saved || at != port
		return v, r, nil
	}
	r := w.resolved(endpoint, result.Replies)
	switch {
	case ctx.Err() != nil:
		return zero, r, ctx.Err()
	case last != nil:
		return zero, r, last
	case r.Conflict || r.Seen != nil:
		// Every address that answered with serial was dialed or
		// disqualified, so with no dial failing, each was disqualified.
		return zero, r, ErrOtherTelescope
	}
	r.NotSeen = true
	v, err := dial(ctx, endpoint)
	return v, r, err
}

// watcher collects a scan's replies for one serial as they arrive: found
// carries each new address that answered with it, at most maxAddresses,
// so a send never blocks. others is each address another serial answered
// from, at most maxOthers, and full that more did.
type watcher struct {
	serial string
	saved  netip.Addr
	found  chan netip.Addr

	mu     sync.Mutex
	seen   *Telescope
	sent   []netip.Addr
	others map[netip.Addr]bool
	full   bool
	other  *Telescope
}

func (w *watcher) observe(t Telescope, from netip.Addr) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t.Serial != w.serial {
		if from == w.saved && w.other == nil {
			t.Addresses = []netip.Addr{from}
			w.other = &t
		}
		switch {
		case w.others[from]:
		case len(w.others) >= maxOthers:
			w.full = true
		default:
			if w.others == nil {
				w.others = make(map[netip.Addr]bool)
			}
			w.others[from] = true
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
	r := Resolved{Endpoint: endpoint, Conflict: w.other != nil, Other: w.other, Replies: replies}
	if w.seen != nil {
		seen := *w.seen
		seen.Addresses = slices.Clone(w.sent)
		r.Seen = &seen
	}
	return r
}

// disqualified reports whether address may not be used: another serial
// answered from it, or from more addresses than the watcher keeps.
func (w *watcher) disqualified(address netip.Addr) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.full || w.others[address]
}
