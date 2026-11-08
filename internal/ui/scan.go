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

// lanState is what the scans saw, by address, so a saved address can keep
// the serial that answered there, and the lock that keeps the copier's
// dials from scanning for a moved telescope more than once at a time.
type lanState struct {
	scanMu   sync.Mutex // one scan from the page at a time
	mu       sync.Mutex // guards seen
	seen     map[netip.Addr]string
	redialMu sync.Mutex
}

func (l *lanState) remember(r landiscovery.ScanResult) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil || len(l.seen) > maxSeen {
		l.seen = make(map[netip.Addr]string)
	}
	for _, t := range r.Telescopes {
		for _, address := range t.Addresses {
			l.seen[address] = t.Serial
		}
	}
}

func (l *lanState) serial(address netip.Addr) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	serial, ok := l.seen[address.Unmap()]
	return serial, ok
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
	scanNoInterface = "This computer has no network to scan. Connect to the telescope's network and Scan again, or type its address."
	scanFailed      = "Couldn't scan this network. Scan again, or type the telescope's address."
)

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
	s.lan.scanMu.Lock()
	var result landiscovery.ScanResult
	var err error
	if probe.IsValid() {
		result, err = lanScanner.Probe(ctx, probe)
	} else {
		result, err = lanScanner.Scan(ctx)
	}
	s.lan.scanMu.Unlock()
	s.lan.remember(result)
	if ctx.Err() != nil {
		return scanView{}, ctx.Err()
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
			view.Message = scanBlocked + scanHint(hostOS)
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

// serialFor is the serial to save with address on this network: the one a
// scan saw there, or the saved one when the address is unchanged.
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

// lanRedial dials t, a target on this network, with dial. When its address
// gets no answer and a serial is saved with it, it scans for the serial,
// dials the telescope where it answered, and saves that address. Activity
// records the model the scan saw and the reply count, never the address
// or the serial.
func lanRedial[T any](ctx context.Context, h *server, t copyapp.Target, dial func(context.Context, string) (T, error)) (T, error) {
	v, err := dial(ctx, t.Endpoint)
	if err == nil || t.Serial == "" || ctx.Err() != nil || !landiscovery.Unreached(err) {
		return v, err
	}
	h.lan.redialMu.Lock()
	defer h.lan.redialMu.Unlock()
	// Another dial may have found the telescope meanwhile.
	if now, terr := h.copyTarget(); terr == nil && now.Run == 0 && now.Device == "" && now.Serial == t.Serial && now.Endpoint != t.Endpoint {
		if v, nerr := dial(ctx, now.Endpoint); nerr == nil {
			return v, nil
		}
		t = now
	}
	v, r, err := landiscovery.Relocate(ctx, lanScanner, t.Endpoint, t.Serial, err, dial)
	if ctx.Err() != nil {
		return v, err
	}
	switch {
	case r.Moved:
		saved := h.controller.persistMoved(t.Endpoint, r.Endpoint, t.Serial)
		text := "Found the telescope" + modelSuffix(r.Seen) + " at a new address on this network (" + replies(r.Replies) + ")"
		if saved {
			h.controller.recordScan(scanMoved, text+" and saved it.")
		} else {
			h.controller.recordScan(scanUnsaved, text+"; the new address wasn't saved.")
		}
	case r.Seen != nil:
		h.controller.recordScan(scanUnreached, "Found the telescope"+modelSuffix(r.Seen)+" on this network ("+replies(r.Replies)+"), but it didn't answer at its new address.")
	default:
		h.controller.recordScan(scanNotFound, "The saved telescope didn't answer, and a scan of this network didn't find it ("+replies(r.Replies)+").")
	}
	return v, err
}

func modelSuffix(t *landiscovery.Telescope) string {
	if t == nil || t.Model == "" {
		return ""
	}
	return " (" + cloudhttp.ModelLabel(t.Model) + ")"
}
