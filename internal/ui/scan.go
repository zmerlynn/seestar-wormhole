package ui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
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
	follows map[string]*following
}

// sighting is the serial the latest scan or probe to finish, numbered gen,
// heard at an address; "" when a probe of the address got no answer.
type sighting struct {
	serial string
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
	set := func(address netip.Addr, serial string) {
		if old, ok := l.seen[address]; !ok || old.gen <= gen {
			l.seen[address] = sighting{serial, gen}
		}
	}
	answered := false
	for _, t := range r.Telescopes {
		for _, address := range t.Addresses {
			set(address, t.Serial)
			answered = answered || address == probed
		}
	}
	if probed.IsValid() && !answered {
		set(probed, "")
	}
}

// serial is the serial last heard at address, if the latest word on it
// was an answer.
func (l *lanState) serial(address netip.Addr) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, ok := l.seen[address.Unmap()]
	return s.serial, ok && s.serial != ""
}

// scanTelescope is one telescope as the page lists it. It has no serial:
// Saved says whether it is the one saved.
type scanTelescope struct {
	Name      string   `json:"name"`
	Model     string   `json:"model"`
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
// lists what answered. Activity records the reply count only.
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
	}
	// A probe has its own socket and stops at the first answer, so it
	// doesn't wait behind a scan.
	var result landiscovery.ScanResult
	var err error
	var gen uint64
	if probe.IsValid() {
		gen = s.lan.begin()
		result, err = lanScanner.Probe(ctx, probe)
	} else {
		s.lan.scanMu.Lock()
		gen = s.lan.begin()
		result, err = lanScanner.Scan(ctx)
		s.lan.scanMu.Unlock()
	}
	if ctx.Err() != nil {
		return scanView{}, ctx.Err()
	}
	if err == nil {
		s.lan.remember(gen, probe, result)
	}
	view := scanView{OK: true, Telescopes: []scanTelescope{}, Replies: result.Replies, Blocked: result.Blocked}
	s.mu.Lock()
	saved := s.settings.LANSerial
	s.mu.Unlock()
	for _, t := range result.Telescopes {
		row := scanTelescope{Name: t.Name, Model: t.Model, Busy: t.Busy(), Saved: saved != "" && t.Serial == saved}
		for _, a := range t.Addresses {
			row.Addresses = append(row.Addresses, a.String())
		}
		view.Telescopes = append(view.Telescopes, row)
	}
	what := "Scanned this network"
	if probe.IsValid() {
		what = "Asked the typed address for a telescope"
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
		s.controller.recordScan(code, fmt.Sprintf("%s: %s.", what, replies(result.Replies)))
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

// lanName is what error text calls a telescope saved with its serial on
// this network, in place of the address a scan found it at.
const lanName = copyapp.SavedLabel

// following is one search for a saved telescope in progress. done closes
// when it ends; endpoint is where it connected, or "".
type following struct {
	done     chan struct{}
	endpoint string
}

// lanFollow connects to t, a target on this network saved with its
// serial, with dial: it scans for the serial and dials where it answers
// (see [landiscovery.Follow]), and saves a new address it connected at.
// It returns the target it reached. Dials for the same target at once
// share one scan: the others wait for it and dial where it connected, and
// search on their own only if that fails. Activity names the model the
// scan saw and the reply count, never an address, a name or the serial.
func lanFollow[T any](ctx context.Context, h *server, t copyapp.Target, dial func(context.Context, string) (T, error)) (T, copyapp.Target, error) {
	key := t.Endpoint + "\x00" + t.Serial
	h.lan.mu.Lock()
	if h.lan.follows == nil {
		h.lan.follows = make(map[string]*following)
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
		return lanSearch(ctx, h, t, dial)
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
	v, reached, err := lanSearch(ctx, h, t, dial)
	if err == nil {
		f.endpoint = reached.Endpoint
	}
	return v, reached, err
}

// lanSearch is one search for t, the scan and its dials, and what it
// saves and records.
func lanSearch[T any](ctx context.Context, h *server, t copyapp.Target, dial func(context.Context, string) (T, error)) (T, copyapp.Target, error) {
	gen := h.controller.routeGeneration()
	v, r, err := landiscovery.Follow(ctx, lanScanner, t.Endpoint, t.Serial, dial)
	if ctx.Err() != nil {
		return v, t, err
	}
	switch {
	case err == nil && r.Moved && h.controller.savedAt(r.Endpoint, t.Serial):
		// A target from before the address was saved: nothing new.
		t.Endpoint = r.Endpoint
	case err == nil && r.Moved:
		saved := h.controller.persistMoved(t.Endpoint, r.Endpoint, t.Serial, gen)
		text := "Found the telescope" + modelSuffix(r.Seen) + " at a new address on this network (" + replies(r.Replies) + ")"
		if saved {
			h.controller.recordScan(scanMoved, text+" and saved it.")
		} else {
			h.controller.recordScan(scanUnsaved, text+"; the new address wasn't saved.")
		}
		t.Endpoint = r.Endpoint
	case r.NotSeen:
		h.controller.recordScan(scanNotSeen, "The saved telescope wasn't seen in a scan of this network; tried its saved address.")
	case r.Conflict:
		h.controller.recordScan(scanOther, "Another telescope answers at the saved address, and a scan of this network didn't find the saved one ("+replies(r.Replies)+"). Scan, then Test and Save it again.")
	case err != nil && r.Seen != nil:
		h.controller.recordScan(scanUnreached, "Found the telescope"+modelSuffix(r.Seen)+" on this network ("+replies(r.Replies)+"), but it didn't connect where it answered.")
	}
	return v, t, err
}

func modelSuffix(t *landiscovery.Telescope) string {
	if t == nil || t.Model == "" {
		return ""
	}
	return " (" + cloudhttp.ModelLabel(t.Model) + ")"
}
