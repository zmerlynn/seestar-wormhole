package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"

	copyapp "github.com/zmerlynn/seestar-wormhole/internal/copy/app"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
)

// lanScanner is how the app scans this network; tests replace it with one
// on fakes.
var lanScanner = &landiscovery.Scanner{}

// maxSeen bounds the addresses the scans remember.
const maxSeen = 256

// lanState is what the page's scans and probes saw, by address, so a
// saved address can keep the serial that answered there, and the
// copier's searches for saved telescopes in progress.
type lanState struct {
	scanMu sync.Mutex // one broadcast scan from the page at a time

	mu      sync.Mutex // guards the rest
	seen    map[netip.Addr]sighting
	gen     uint64
	follows map[followKey]*following
	probes  map[netip.Addr]*lanProbe
}

// lanProbe is a probe of one address in flight. The page's probe of a
// typed address and Test's serial hook share it, so the two get one
// result rather than two that may disagree.
type lanProbe struct {
	done   chan struct{} // closed once result and err are set
	result landiscovery.ScanResult
	err    error
}

// maxProbes bounds the probes in flight.
const maxProbes = 16

// errProbes is a probe refused because maxProbes are in flight.
var errProbes = errors.New("too many probes of typed addresses at once")

// probe probes address, an IPv4 address, or joins the probe of it in
// flight, and returns what that probe heard. The probe is numbered once
// and remembered once, as it ends, whichever callers are still waiting;
// it runs to its own end even when they stop waiting, and each stops
// waiting when its ctx ends.
func (l *lanState) probe(ctx context.Context, address netip.Addr) (landiscovery.ScanResult, error) {
	l.mu.Lock()
	p := l.probes[address]
	if p == nil {
		if len(l.probes) >= maxProbes {
			l.mu.Unlock()
			return landiscovery.ScanResult{}, errProbes
		}
		if l.probes == nil {
			l.probes = make(map[netip.Addr]*lanProbe)
		}
		l.gen++
		gen, scanner := l.gen, lanScanner
		p = &lanProbe{done: make(chan struct{})}
		l.probes[address] = p
		go func() {
			p.result, p.err = scanner.Probe(context.WithoutCancel(ctx), address)
			if p.err == nil {
				l.remember(gen, address, p.result)
			}
			l.mu.Lock()
			delete(l.probes, address)
			l.mu.Unlock()
			close(p.done)
		}()
	}
	l.mu.Unlock()
	select {
	case <-p.done:
		return p.result, p.err
	case <-ctx.Done():
		return landiscovery.ScanResult{}, ctx.Err()
	}
}

// sighting is the serial the latest scan or probe to finish, numbered gen,
// heard at an address; "" when a probe of the address got no answer.
type sighting struct {
	serial string
	model  string // the model it answered with, for naming its folder
	gen    uint64
}

// begin numbers a scan or probe as it starts.
func (l *lanState) begin() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.gen++
	return l.gen
}

// remember records what the scan or probe numbered gen heard. A probe of
// probed that heard nothing there records that too. A result never
// replaces one from a scan or probe that started later.
func (l *lanState) remember(gen uint64, probed netip.Addr, r landiscovery.ScanResult) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil || len(l.seen) > maxSeen {
		l.seen = make(map[netip.Addr]sighting)
	}
	set := func(address netip.Addr, serial, model string) {
		if old, ok := l.seen[address]; !ok || old.gen <= gen {
			l.seen[address] = sighting{serial, model, gen}
		}
	}
	answered := false
	for _, t := range r.Telescopes {
		for _, address := range t.Addresses {
			set(address, t.Serial, t.Model)
			answered = answered || address == probed
		}
	}
	if probed.IsValid() && !answered {
		set(probed, "", "")
	}
}

// modelOf is the model the latest scan or probe heard with serial, or "".
func (l *lanState) modelOf(serial string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var best sighting
	for _, s := range l.seen {
		if s.serial == serial && s.model != "" && s.gen >= best.gen {
			best = s
		}
	}
	return best.model
}

// modelAt is the model last heard at address, or "".
func (l *lanState) modelAt(address netip.Addr) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seen[address.Unmap()].model
}

// serial is the serial last heard at address, if the latest word on it
// was an answer.
func (l *lanState) serial(address netip.Addr) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, ok := l.seen[address.Unmap()]
	return s.serial, ok && s.serial != ""
}

// serialAt reports whether the latest scan or probe heard a serial at
// host, an IPv4 address: Test's [copyapp.MountOptions.SerialAt]. When
// none has heard one there and scans aren't set never, it probes host,
// sharing the page's own probe beside Test when it is in flight, without a
// line in Activity.
func (s *server) serialAt(ctx context.Context, host string) bool {
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.Unmap().Is4() {
		return false
	}
	ip = ip.Unmap()
	if _, ok := s.lan.serial(ip); ok {
		return true
	}
	if s.lanScan() == landiscovery.ScanOff {
		return false
	}
	if _, err := s.lan.probe(ctx, ip); err != nil {
		return false
	}
	_, ok := s.lan.serial(ip)
	return ok
}

// scanTelescope is one telescope as the page lists it. Saved says whether
// it is the one saved.
type scanTelescope struct {
	Name      string   `json:"name"`
	Model     string   `json:"model"`
	Serial    string   `json:"serial"`
	Addresses []string `json:"addresses"`
	Busy      bool     `json:"busy"`
	Saved     bool     `json:"saved"`
}

// recordScan records a scan line in Activity and gui.log.
func (c *controller) recordScan(code, text string) {
	if c.log != nil {
		c.log.addScan(code, text)
	}
}

// scanView is /api/scan's answer. Message is set when nothing answered a
// scan of the whole network, or the scan couldn't run.
type scanView struct {
	OK         bool            `json:"ok"`
	Telescopes []scanTelescope `json:"telescopes"`
	Replies    int             `json:"replies"`
	Blocked    bool            `json:"blocked"`
	Message    string          `json:"message"`
}

// The scan's page messages.
const (
	scanNone        = "No telescope answered."
	scanBlocked     = "Local Network access is off for seestar-wormhole."
	scanNoRoute     = "Couldn't send a scan on this network (no route to host). Check the network connection and Scan again, or type the telescope's address."
	scanNoInterface = "This computer has no network to scan. Connect to the telescope's network and Scan again, or type its address."
	scanFailed      = "Couldn't scan this network. Scan again, or type the telescope's address."
)

// blockedMessage is the page's message for a scan whose sends failed with
// "no route to host" and that heard nothing: on macOS, Local Network
// access denied; elsewhere, a network problem.
func blockedMessage(goos string) string {
	if goos != "darwin" {
		return scanNoRoute
	}
	return scanBlocked + scanHint(goos)
}

// scanHint is the Local Network hint for a scan on macOS.
func scanHint(goos string) string {
	if goos != "darwin" {
		return ""
	}
	return " On macOS, if Local Network access is blocked, enable seestar-wormhole in System Settings > Privacy & Security > Local Network, then Scan again. If a permission request is open, allow access first."
}

// scan scans this network, or asks address alone when one is given, and
// lists what answered. Activity records the reply count, then a line
// naming each telescope that answered, with its addresses.
func (s *server) scan(ctx context.Context, address string) (scanView, error) {
	var probe netip.Addr
	if address != "" {
		normalized, ok := copyapp.NormalizeAddress(address)
		if !ok {
			return scanView{}, &ActionError{Code: "lan_address", Field: "lan_address"}
		}
		host, _, _ := net.SplitHostPort(normalized)
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.Unmap().Is4() {
			// A name or an IPv6 address: only Test checks it.
			return scanView{OK: true, Telescopes: []scanTelescope{}}, nil
		}
		probe = ip.Unmap()
		if s.lanScan() == landiscovery.ScanOff {
			// Nothing goes on the network unasked: only Test checks it.
			return scanView{OK: true, Telescopes: []scanTelescope{}}, nil
		}
	}
	// A probe has its own socket and stops at the first answer, so it
	// doesn't wait behind a scan; Test's serial hook shares it.
	var result landiscovery.ScanResult
	var err error
	if probe.IsValid() {
		result, err = s.lan.probe(ctx, probe)
	} else {
		s.lan.scanMu.Lock()
		gen := s.lan.begin()
		result, err = lanScanner.Scan(ctx)
		s.lan.scanMu.Unlock()
		if err == nil && ctx.Err() == nil {
			s.lan.remember(gen, probe, result)
		}
	}
	if ctx.Err() != nil {
		return scanView{}, ctx.Err()
	}
	view := scanView{OK: true, Telescopes: []scanTelescope{}, Replies: result.Replies, Blocked: result.Blocked}
	s.mu.Lock()
	saved := s.settings.LANSerial
	s.mu.Unlock()
	for _, t := range result.Telescopes {
		row := scanTelescope{Name: t.Name, Model: t.Model, Serial: t.Serial, Busy: t.Busy(), Saved: saved != "" && t.Serial == saved}
		for _, a := range t.Addresses {
			row.Addresses = append(row.Addresses, a.String())
		}
		view.Telescopes = append(view.Telescopes, row)
	}
	what := "Scanned this network"
	if probe.IsValid() {
		what = "Asked " + probe.String() + " for a telescope"
	}
	code := scanListed
	if probe.IsValid() {
		code = scanProbed
	}
	switch {
	case errors.Is(err, landiscovery.ErrNoInterface):
		s.controller.recordScan(scanFailedCode, what+": no network to scan.")
		view.Message = scanNoInterface
	case err != nil:
		s.controller.recordScan(scanFailedCode, what+": the scan failed.")
		view.Message = scanFailed
	default:
		// A line for the count and one for each telescope, so none
		// outgrows the log.
		s.controller.recordScan(code, what+": "+replies(result.Replies)+".")
		for _, t := range result.Telescopes {
			s.controller.recordScan(code, "Found "+t.Label()+" at "+joinAddrs(t.Addresses)+".")
		}
		if !probe.IsValid() && result.Blocked {
			view.Message = blockedMessage(hostOS)
		} else if !probe.IsValid() && len(result.Telescopes) == 0 {
			view.Message = scanNone + scanHint(hostOS)
		}
	}
	return view, nil
}

func replies(n int) string {
	if n == 1 {
		return "1 reply"
	}
	return fmt.Sprintf("%d replies", n)
}

// serialFor is the serial to save with address on this network: the one
// the latest scan or probe heard there, or, when the latest word is no
// answer or there is none, the saved one if the address is unchanged.
// Every connect checks the serial again, so a saved one is never trusted
// on its own.
func (s *server) serialFor(route, address string) string {
	if route != routeLAN {
		return ""
	}
	if host, _, err := net.SplitHostPort(address); err == nil {
		if ip, err := netip.ParseAddr(host); err == nil {
			if serial, ok := s.lan.serial(ip); ok {
				return serial
			}
		}
	}
	s.mu.Lock()
	saved := s.settings
	s.mu.Unlock()
	if old, ok := copyapp.NormalizeAddress(saved.LANAddress); ok && old == address {
		return saved.LANSerial
	}
	return ""
}

// lanConnect connects to t, a target on this network saved with its
// serial, with dial, as the saved LANScan says: off dials t's endpoint
// alone; connect follows the telescope (see [lanFollow]); start follows
// it once in the run ctx carries (see [landiscovery.InRun]) and dials
// where it connected after that, following it again when that fails. A
// connect outside a run follows the telescope. Every follow is under the
// routeGeneration read with the mode. It returns the target it reached.
func lanConnect[T io.Closer](ctx context.Context, h *server, t copyapp.Target, dial func(context.Context, string) (T, error)) (T, copyapp.Target, error) {
	mode, gen := h.lanPolicy()
	run := landiscovery.RunFrom(ctx)
	switch {
	case mode == landiscovery.ScanOff:
		v, err := dial(ctx, t.Endpoint)
		return v, t, err
	case mode == landiscovery.ScanConnect || run == nil:
		return lanFollow(ctx, h, t, gen, dial)
	}
	// The run remembers the telescope by its serial and the explicit save
	// it was found under: once a scan saved a new address, the next
	// connect's target carries it, and after a route save or a change of
	// mode, the run's earlier finding is not reused.
	v, at, _, err := landiscovery.InRunAt(ctx, run, t.Serial, gen, dial, func(ctx context.Context) (T, string, error) {
		v, reached, err := lanFollow(ctx, h, t, gen, dial)
		return v, reached.Endpoint, err
	})
	if err == nil {
		t.Endpoint = at
	}
	return v, t, err
}

// following is one search for a saved telescope in progress. done closes
// when it ends; endpoint is where it connected, or "".
type following struct {
	done     chan struct{}
	endpoint string
}

// followKey is what dials share a search by: the target and the explicit
// save it was made under, so a dial after a newer save never waits for
// an older search's finding.
type followKey struct {
	endpoint, serial string
	gen              uint64
}

// lanFollow connects to t, a target on this network saved with its
// serial, with dial: it scans for the serial and dials where it answers
// (see [landiscovery.Follow]), and saves a new address it connected at.
// It returns the target it reached. Dials for the same target under the
// same routeGeneration gen at once share one scan: the others wait for it
// and dial where it connected, and search on their own only if that
// fails. Activity names the telescope, the addresses and the reply count.
func lanFollow[T io.Closer](ctx context.Context, h *server, t copyapp.Target, gen uint64, dial func(context.Context, string) (T, error)) (T, copyapp.Target, error) {
	key := followKey{t.Endpoint, t.Serial, gen}
	h.lan.mu.Lock()
	if h.lan.follows == nil {
		h.lan.follows = make(map[followKey]*following)
	}
	if f := h.lan.follows[key]; f != nil {
		h.lan.mu.Unlock()
		select {
		case <-f.done:
		case <-ctx.Done():
			var zero T
			return zero, t, ctx.Err()
		}
		if f.endpoint != "" {
			if v, err := dial(ctx, f.endpoint); err == nil {
				t.Endpoint = f.endpoint
				return v, t, nil
			}
		}
		return lanSearch(ctx, h, t, gen, dial)
	}
	f := &following{done: make(chan struct{})}
	h.lan.follows[key] = f
	h.lan.mu.Unlock()
	defer func() {
		h.lan.mu.Lock()
		delete(h.lan.follows, key)
		h.lan.mu.Unlock()
		close(f.done)
	}()
	v, reached, err := lanSearch(ctx, h, t, gen, dial)
	if err == nil {
		f.endpoint = reached.Endpoint
	}
	return v, reached, err
}

// lanSearch is one search for t, the scan and its dials, and what it
// saves and records. It saves a new address only when no explicit save
// came after gen, the routeGeneration its connect was made under.
func lanSearch[T io.Closer](ctx context.Context, h *server, t copyapp.Target, gen uint64, dial func(context.Context, string) (T, error)) (T, copyapp.Target, error) {
	v, r, err := landiscovery.Follow(ctx, lanScanner, t.Endpoint, t.Serial, dial, func(v T) { v.Close() })
	if ctx.Err() != nil {
		return v, t, err
	}
	switch {
	case err == nil && r.Moved && h.controller.savedAt(r.Endpoint, t.Serial):
		// A target from before the address was saved: nothing new.
		t.Endpoint = r.Endpoint
	case err == nil && r.Moved:
		saved := h.controller.persistMoved(t.Endpoint, r.Endpoint, t.Serial, gen)
		text := "Found " + named(r.Seen, t.Serial) + " at " + r.Endpoint + ", not its saved " + t.Endpoint + ", in a scan of this network (" + replies(r.Replies) + ")"
		if saved {
			h.controller.recordScan(scanMoved, text+", and saved it.")
		} else {
			h.controller.recordScan(scanUnsaved, text+"; the new address wasn't saved.")
		}
		t.Endpoint = r.Endpoint
	case r.NotSeen:
		h.controller.recordScan(scanNotSeen, named(nil, t.Serial)+" wasn't seen in a scan of this network ("+replies(r.Replies)+"); tried its saved address, "+t.Endpoint+".")
	case r.Conflict && r.Seen == nil:
		h.controller.recordScan(scanOther, named(r.Other, "")+" answers at the saved address, "+t.Endpoint+", and a scan of this network didn't find "+named(nil, t.Serial)+" ("+replies(r.Replies)+"). Scan, then Test and Save it again.")
	case err != nil && r.Seen != nil:
		h.controller.recordScan(scanUnreached, "Found "+named(r.Seen, t.Serial)+" on this network ("+replies(r.Replies)+"), but it didn't connect where it answered, at "+joinAddrs(r.Seen.Addresses)+".")
	}
	return v, t, err
}

// named is how Activity names t (see [landiscovery.Telescope.Label]),
// or, when the scan didn't see it, the telescope with serial, or another
// telescope for neither.
func named(t *landiscovery.Telescope, serial string) string {
	switch {
	case t != nil:
		return t.Label()
	case serial != "":
		return landiscovery.Telescope{Serial: serial}.Label()
	}
	return "Another telescope"
}

// joinAddrs is addrs separated by commas.
func joinAddrs(addrs []netip.Addr) string {
	var b strings.Builder
	for i, a := range addrs {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(a.String())
	}
	return b.String()
}
