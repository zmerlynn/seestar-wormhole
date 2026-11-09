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

	"github.com/zmerlynn/pktinfo"
	"golang.org/x/net/ipv4"
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

// errLocal is a scan that couldn't list this computer's own addresses, so
// it couldn't tell a reply from a proxy here from a telescope's.
var errLocal = errors.New("LAN scan: couldn't determine this computer's addresses")

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

// Conn is the part of a UDP socket a scan uses. WriteTo sends with the
// packet information in cm, when it is not nil: a broadcast names the
// interface and the source address it goes out from.
type Conn interface {
	ReadFrom([]byte) (int, net.Addr, error)
	WriteTo(b []byte, cm *ipv4.ControlMessage, dst net.Addr) (int, error)
	SetReadDeadline(time.Time) error
	Close() error
}

// udpConn is a Conn on a UDP socket, whose sends use the platform's packet
// information support.
type udpConn struct {
	*net.UDPConn
	packet *pktinfo.IPv4PacketConn
}

func (c udpConn) WriteTo(b []byte, cm *ipv4.ControlMessage, dst net.Addr) (int, error) {
	return c.packet.WriteTo(b, cm, dst)
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
	return s.broadcast(ctx, nil, nil)
}

// watch is Scan, calling observe with each accepted reply, its telescope
// and the address it came from, as it arrives. observe runs on the scan's
// goroutine and must not block.
func (s *Scanner) watch(ctx context.Context, observe func(Telescope, netip.Addr)) (ScanResult, error) {
	return s.broadcast(ctx, nil, observe)
}

// Probe sends the request straight to address and returns once it answers.
// Only a reply from address counts.
func (s *Scanner) Probe(ctx context.Context, address netip.Addr) (ScanResult, error) {
	address = address.Unmap()
	if !address.Is4() {
		return ScanResult{}, fmt.Errorf("LAN scan: %s is not an IPv4 address", address)
	}
	_, local, err := s.inventory()
	if err != nil {
		return ScanResult{}, err
	}
	return s.run(ctx, []target{{address: address}}, local, func(from netip.Addr) bool { return from == address },
		func(r *ScanResult) bool { return len(r.Telescopes) > 0 }, nil)
}

func (s *Scanner) source() Interfaces {
	if s.Interfaces == nil {
		return netInterfaceSource{}
	}
	return s.Interfaces
}

func (s *Scanner) broadcast(ctx context.Context, done func(*ScanResult) bool, observe func(Telescope, netip.Addr)) (ScanResult, error) {
	targets, local, err := s.inventory()
	if err != nil {
		return ScanResult{}, err
	}
	if len(targets) == 0 {
		return ScanResult{}, ErrNoInterface
	}
	return s.run(ctx, targets, local, func(netip.Addr) bool { return true }, done, observe)
}

// target is where one send of a scan goes: a directed broadcast, sent out
// of the interface numbered index from the source address src, or, for a
// probe, one address with no interface chosen (index 0).
type target struct {
	address netip.Addr
	index   int
	src     netip.Addr
}

// inventory reads the interfaces once and returns both the scan's targets
// and every address of this computer, so a proxy here that answers scans
// for the phone app is not listed as a telescope. The targets are the
// directed broadcasts of the eligible interfaces: up, broadcast-capable,
// neither loopback nor point-to-point, one per subnet on each interface.
// It fails if any interface's addresses can't be read: the own-address
// filter needs all of them.
func (s *Scanner) inventory() ([]target, map[netip.Addr]bool, error) {
	source := s.source()
	interfaces, err := source.List()
	if err != nil {
		return nil, nil, errLocal
	}
	var targets []target
	local := make(map[netip.Addr]bool)
	for _, iface := range interfaces {
		addresses, err := source.Addrs(&iface)
		if err != nil {
			return nil, nil, errLocal
		}
		eligible := eligibleInterface(iface.Flags, Options{}) && iface.Flags&net.FlagPointToPoint == 0 && iface.Index > 0
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil {
				continue
			}
			local[prefix.Addr().Unmap()] = true
			if !eligible || !prefix.Addr().Is4() || !prefix.Addr().IsGlobalUnicast() || prefix.Bits() >= 31 {
				continue
			}
			t := target{address: broadcast(prefix), index: iface.Index, src: prefix.Addr()}
			if !slices.ContainsFunc(targets, func(u target) bool { return u.index == t.index && u.address == t.address }) {
				targets = append(targets, t)
			}
		}
	}
	return targets, local, nil
}

func (s *Scanner) listen() (Conn, error) {
	if s.Listen != nil {
		return s.Listen()
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	return udpConn{UDPConn: conn, packet: pktinfo.NewIPv4PacketConn(conn)}, nil
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
// the window closes, ctx is done, or done reports true. observe, if set,
// sees each accepted reply as it arrives.
func (s *Scanner) run(ctx context.Context, targets []target, local map[netip.Addr]bool, accept func(netip.Addr) bool, done func(*ScanResult) bool, observe func(Telescope, netip.Addr)) (ScanResult, error) {
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
				var cm *ipv4.ControlMessage
				if target.index > 0 {
					cm = &ipv4.ControlMessage{IfIndex: target.index, Src: net.IP(target.src.AsSlice())}
				}
				if _, err := conn.WriteTo(request, cm, net.UDPAddrFromAddrPort(netip.AddrPortFrom(target.address, Port))); err != nil {
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
		if observe != nil {
			observe(t, address)
		}
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
	// Invalid UTF-8 in a label reads as U+FFFD, as in the outer decode.
	var name, model string
	if json.Unmarshal(v.Result.SSID, &name, jsontext.AllowInvalidUTF8(true)) == nil {
		t.Name = name
	}
	if json.Unmarshal(v.Result.Model, &model, jsontext.AllowInvalidUTF8(true)) == nil {
		t.Model = model
	}
	var clients int
	if json.Unmarshal(v.Result.Clients, &clients) == nil {
		t.Clients = clients
	}
	return t, true
}
