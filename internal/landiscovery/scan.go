package landiscovery

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sync"
	"syscall"
	"time"
)

// The scan's defaults: three sends 2 s apart, and 6 s of listening in all.
// The phone app sends up to ten times over 20 s; a desktop on a wired or
// steady Wi-Fi link needs fewer.
const (
	DefaultSends    = 3
	DefaultInterval = 2 * time.Second
	DefaultWindow   = 6 * time.Second
)

// BusyClients is the client count at which the phone app refuses to
// connect. File copying is not held to that limit.
const BusyClients = 8

// The scan keeps at most this many telescopes and addresses per telescope;
// further replies are counted and otherwise ignored.
const (
	maxTelescopes = 64
	maxAddresses  = 8
)

// ErrNoInterface is a broadcast scan with no up, broadcast-capable IPv4
// interface to send from.
var ErrNoInterface = errors.New("LAN scan: no network interface to scan from")

// Telescope is one serial number seen in a scan, with every address that
// answered for it. Serial identifies the telescope and is never shown or
// logged; Name is the reply's ssid.
type Telescope struct {
	Serial    string
	Name      string
	Model     string
	Addresses []netip.Addr
	Clients   int
}

// Busy reports whether the telescope reported as many clients as the phone
// app allows.
func (t Telescope) Busy() bool { return t.Clients >= BusyClients }

// ScanResult is what a scan heard. Replies counts the accepted replies.
// Blocked means a send failed with "no route to host" and nothing answered:
// on macOS that is how a Local Network denial shows.
type ScanResult struct {
	Telescopes []Telescope
	Replies    int
	Blocked    bool
}

// Find returns the telescope with serial, if the scan saw it.
func (r ScanResult) Find(serial string) (Telescope, bool) {
	for _, t := range r.Telescopes {
		if serial != "" && t.Serial == serial {
			return t, true
		}
	}
	return Telescope{}, false
}

// Conn is the part of a UDP socket a scan uses. A *net.UDPConn is one.
type Conn interface {
	ReadFrom([]byte) (int, net.Addr, error)
	WriteTo([]byte, net.Addr) (int, error)
	SetReadDeadline(time.Time) error
	Close() error
}

// Scanner sends the phone app's scan_iscope request and collects the
// replies. Its zero value scans the operating system's interfaces with the
// default timers; tests replace Interfaces and Listen so they never touch
// the network.
type Scanner struct {
	Interfaces Interfaces           // nil uses the operating system's
	Listen     func() (Conn, error) // nil opens an ephemeral IPv4 UDP socket
	Sends      int                  // zero means DefaultSends
	Interval   time.Duration        // zero means DefaultInterval
	Window     time.Duration        // zero means DefaultWindow
}

// Scan broadcasts the request on each eligible interface and returns every
// telescope that answered within the window.
func (s *Scanner) Scan(ctx context.Context) (ScanResult, error) {
	return s.broadcast(ctx, nil)
}

// Find broadcasts like Scan but returns as soon as serial answers.
func (s *Scanner) Find(ctx context.Context, serial string) (ScanResult, error) {
	return s.broadcast(ctx, func(r *ScanResult) bool {
		_, ok := r.Find(serial)
		return ok
	})
}

// Probe sends the request straight to address and returns once it answers.
// Only a reply from address counts.
func (s *Scanner) Probe(ctx context.Context, address netip.Addr) (ScanResult, error) {
	address = address.Unmap()
	if !address.Is4() {
		return ScanResult{}, fmt.Errorf("LAN scan: %s is not an IPv4 address", address)
	}
	local, err := s.local()
	if err != nil {
		return ScanResult{}, err
	}
	return s.run(ctx, []netip.Addr{address}, local, func(from netip.Addr) bool { return from == address },
		func(r *ScanResult) bool { return len(r.Telescopes) > 0 })
}

func (s *Scanner) source() Interfaces {
	if s.Interfaces == nil {
		return netInterfaceSource{}
	}
	return s.Interfaces
}

func (s *Scanner) broadcast(ctx context.Context, done func(*ScanResult) bool) (ScanResult, error) {
	targets, err := s.targets()
	if err != nil {
		return ScanResult{}, err
	}
	local, err := s.local()
	if err != nil {
		return ScanResult{}, err
	}
	return s.run(ctx, targets, local, func(netip.Addr) bool { return true }, done)
}

// targets are the directed broadcasts of the eligible interfaces: up,
// broadcast-capable, neither loopback nor point-to-point.
func (s *Scanner) targets() ([]netip.Addr, error) {
	source := s.source()
	interfaces, err := source.List()
	if err != nil {
		return nil, err
	}
	var targets []netip.Addr
	for _, iface := range interfaces {
		if !eligibleInterface(iface.Flags, Options{}) || iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		addresses, err := source.Addrs(&iface)
		if err != nil {
			continue
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsGlobalUnicast() || prefix.Bits() >= 31 {
				continue
			}
			if b := broadcast(prefix); !slices.Contains(targets, b) {
				targets = append(targets, b)
			}
		}
	}
	if len(targets) == 0 {
		return nil, ErrNoInterface
	}
	return targets, nil
}

// local lists every address of this computer, so a proxy here that answers
// scans for the phone app is not listed as a telescope.
func (s *Scanner) local() (map[netip.Addr]bool, error) {
	source := s.source()
	interfaces, err := source.List()
	if err != nil {
		return nil, err
	}
	local := make(map[netip.Addr]bool)
	for _, iface := range interfaces {
		addresses, err := source.Addrs(&iface)
		if err != nil {
			continue
		}
		for _, address := range addresses {
			if prefix, err := netip.ParsePrefix(address.String()); err == nil {
				local[prefix.Addr().Unmap()] = true
			}
		}
	}
	return local, nil
}

func (s *Scanner) listen() (Conn, error) {
	if s.Listen != nil {
		return s.Listen()
	}
	return net.ListenUDP("udp4", &net.UDPAddr{})
}

func (s *Scanner) timers() (sends int, interval, window time.Duration) {
	sends, interval, window = s.Sends, s.Interval, s.Window
	if sends <= 0 {
		sends = DefaultSends
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	if window <= 0 {
		window = DefaultWindow
	}
	return sends, interval, window
}

// run sends the request to each target sends times and reads replies until
// the window closes, ctx is done, or done reports true.
func (s *Scanner) run(ctx context.Context, targets []netip.Addr, local map[netip.Addr]bool, accept func(netip.Addr) bool, done func(*ScanResult) bool) (ScanResult, error) {
	var result ScanResult
	conn, err := s.listen()
	if err != nil {
		return result, fmt.Errorf("LAN scan: %w", err)
	}
	defer conn.Close()

	// A done ctx moves the read deadline into the past. The mutex keeps the
	// loop from moving it forward again afterward.
	var mu sync.Mutex
	cancelled := false
	stop := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		cancelled = true
		conn.SetReadDeadline(time.Unix(1, 0))
	})
	defer stop()

	sends, interval, window := s.timers()
	start := time.Now()
	end := start.Add(window)
	sent, attempts, failed, blocked := 0, 0, 0, 0
	var sendErr error
	buf := make([]byte, MaxDatagram+1)
	for {
		now := time.Now()
		for sent < sends && !now.Before(start.Add(time.Duration(sent)*interval)) {
			sent++
			request := fmt.Appendf(nil, `{"id":%d,"method":"scan_iscope","params":""}`+"\r\n", sent)
			for _, target := range targets {
				attempts++
				if _, err := conn.WriteTo(request, net.UDPAddrFromAddrPort(netip.AddrPortFrom(target, Port))); err != nil {
					failed++
					if errors.Is(err, syscall.EHOSTUNREACH) {
						blocked++
					} else if sendErr == nil {
						sendErr = err
					}
				}
			}
		}
		if !now.Before(end) || (done != nil && done(&result)) {
			break
		}
		deadline := end
		if next := start.Add(time.Duration(sent) * interval); sent < sends && next.Before(end) {
			deadline = next
		}
		mu.Lock()
		if cancelled {
			mu.Unlock()
			break
		}
		conn.SetReadDeadline(deadline)
		mu.Unlock()
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if errors.Is(err, net.ErrClosed) {
				return result, fmt.Errorf("LAN scan: %w", err)
			}
			continue // a deadline, or an error the next send may clear
		}
		if n > MaxDatagram {
			continue
		}
		address, ok := sourceAddr(from)
		if !ok || local[address] || !accept(address) {
			continue
		}
		t, ok := decodeReply(buf[:n])
		if !ok {
			continue
		}
		result.Replies++
		result.add(t, address)
	}
	if result.Replies == 0 && blocked > 0 {
		result.Blocked = true
	} else if result.Replies == 0 && attempts > 0 && failed == attempts {
		return result, fmt.Errorf("LAN scan: %w", unaddressed(sendErr))
	}
	return result, ctx.Err()
}

// unaddressed is a send's error without the addresses a *net.OpError
// names, so its text can be logged.
func unaddressed(err error) error {
	if op, ok := errors.AsType[*net.OpError](err); ok && op.Err != nil {
		return fmt.Errorf("%s: %w", op.Op, op.Err)
	}
	return err
}

func sourceAddr(from net.Addr) (netip.Addr, bool) {
	udp, ok := from.(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, false
	}
	address, ok := netip.AddrFromSlice(udp.IP)
	address = address.Unmap()
	return address, ok && address.Is4()
}

func (r *ScanResult) add(t Telescope, address netip.Addr) {
	for i := range r.Telescopes {
		known := &r.Telescopes[i]
		if known.Serial != t.Serial {
			continue
		}
		known.Clients = t.Clients
		if !slices.Contains(known.Addresses, address) && len(known.Addresses) < maxAddresses {
			known.Addresses = append(known.Addresses, address)
		}
		return
	}
	if len(r.Telescopes) < maxTelescopes {
		t.Addresses = []netip.Addr{address}
		r.Telescopes = append(r.Telescopes, t)
	}
}

// decodeReply reads a reply the way the phone app accepts one: result.sn
// must be a non-empty string. The other members it reads are optional and
// a member of the wrong type reads as absent. The reply's password member
// has no field here, so it is never decoded.
func decodeReply(b []byte) (Telescope, bool) {
	var v struct {
		Result *struct {
			SN      jsontext.Value `json:"sn"`
			SSID    jsontext.Value `json:"ssid"`
			Model   jsontext.Value `json:"product_model"`
			Clients jsontext.Value `json:"tcp_client_num"`
		} `json:"result"`
	}
	if json.Unmarshal(b, &v, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)) != nil || v.Result == nil {
		return Telescope{}, false
	}
	var t Telescope
	if json.Unmarshal(v.Result.SN, &t.Serial) != nil || t.Serial == "" {
		return Telescope{}, false
	}
	var name, model string
	if json.Unmarshal(v.Result.SSID, &name) == nil {
		t.Name = name
	}
	if json.Unmarshal(v.Result.Model, &model) == nil {
		t.Model = model
	}
	var clients int
	if json.Unmarshal(v.Result.Clients, &clients) == nil {
		t.Clients = clients
	}
	return t, true
}
