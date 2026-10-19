package landiscovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"golang.org/x/net/ipv4"
)

// Socket owns an IPv4 wildcard receiver and the LAN interface/source selection.
// Wildcard reception is required for directed and limited broadcast; binding
// only the unicast address can miss both, so the socket binds to any address.
// Reachable unicast source addresses are required; wildcard binding is our local edge choice:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#stand-in-responders
type Socket struct {
	Logger     *slog.Logger
	conn       *net.UDPConn
	packet     packetConn
	interfaces map[int][]netip.Prefix
	addresses  func(int) []netip.Prefix
	elapsed    func() time.Duration // optional edge clock; set before Serve
	// Serve's single owner spends inspection work even when admission rejects.
	inspectionUsed int
	inspectionLast time.Duration
	summaryLast    time.Duration
}

type packetConn interface {
	ReadFrom([]byte) (int, *ipv4.ControlMessage, net.Addr, error)
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	WriteTo([]byte, *ipv4.ControlMessage, net.Addr) (int, error)
}

// Local work policy: four immediate inspections, then one per second per
// socket, independently of reply tokens. The zero value starts with a full burst.
const inspectionBurst = 4

func (s *Socket) takeInspection(now time.Duration) bool {
	now = max(now, s.inspectionLast)
	delta := now - s.inspectionLast
	if delta >= time.Second {
		s.inspectionUsed = max(0, s.inspectionUsed-int(min(delta/time.Second, inspectionBurst)))
		s.inspectionLast = now - delta%time.Second
	}
	if s.inspectionUsed == inspectionBurst {
		return false
	}
	s.inspectionUsed++
	return true
}

// Options selects which local adapters may answer discovery scans.
type Options struct {
	AllInterfaces bool // Include non-broadcast adapters; loopback is always excluded.
}

func eligibleInterface(flags net.Flags, options Options) bool {
	return flags&net.FlagUp != 0 && flags&net.FlagLoopback == 0 &&
		(options.AllInterfaces || flags&net.FlagBroadcast != 0)
}

// ValidBind reports whether bind is a literal IPv4 LAN candidate or 0.0.0.0.
// Listen additionally verifies ownership by an eligible active interface.
func ValidBind(bind string) bool {
	ip, err := netip.ParseAddr(bind)
	return err == nil && ip.Is4() && (ip.IsUnspecified() || (ip.IsGlobalUnicast() && !ip.IsLoopback()))
}

// Listen binds UDP port on the selected interface(s). Production uses Port;
// port zero permits isolated tests without sharing the app's real listener.
func Listen(bind string, port int, options Options) (*Socket, error) {
	if !ValidBind(bind) {
		return nil, errors.New("LAN discovery: bind must be a local LAN IPv4 address or 0.0.0.0")
	}
	selected, _ := netip.ParseAddr(bind)
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	sources := make(map[int][]netip.Prefix)
	for _, iface := range interfaces {
		if !eligibleInterface(iface.Flags, options) {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsGlobalUnicast() {
				continue
			}
			if selected.IsUnspecified() || selected == prefix.Addr() {
				// Preserve the host address as well as its on-link prefix.
				// New addresses require restarting the responder.
				sources[iface.Index] = append(sources[iface.Index], prefix)
			}
		}
	}
	if len(sources) == 0 {
		if options.AllInterfaces {
			return nil, errors.New("LAN discovery: bind has no active local nonloopback interface")
		}
		return nil, errors.New("LAN discovery: bind has no active local broadcast interface")
	}
	s, err := listen(port, sources)
	if err != nil {
		return nil, err
	}
	s.addresses = func(index int) []netip.Prefix { return interfacePrefixes(index, options) }
	return s, nil
}

// interfacePrefixes fails closed if the ingress interface is gone, inactive,
// or cannot be inspected. Read per eligible scan so a prefix change cannot leave
// an obsolete on-link admission boundary in use.
func interfacePrefixes(index int, options Options) []netip.Prefix {
	iface, err := net.InterfaceByIndex(index)
	if err != nil || !eligibleInterface(iface.Flags, options) {
		return nil
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return nil
	}
	var prefixes []netip.Prefix
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err == nil && prefix.Addr().Is4() && prefix.Addr().IsGlobalUnicast() {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}

func listen(port int, sources map[int][]netip.Prefix) (*Socket, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		return nil, err
	}
	packet := ipv4.NewPacketConn(conn)
	if err := packet.SetControlMessage(ipv4.FlagInterface|ipv4.FlagDst, true); err != nil {
		conn.Close()
		return nil, err
	}
	// Private socket tests supply synthetic loopback addresses through this seam;
	// public Listen installs the live interface reader before returning.
	return &Socket{conn: conn, packet: packet, interfaces: sources,
		addresses: func(index int) []netip.Prefix { return sources[index] }}, nil
}

func (s *Socket) currentPrefixes(index int) []netip.Prefix {
	selected := s.interfaces[index]
	if len(selected) == 0 {
		return nil
	}
	current := s.addresses(index)
	var prefixes []netip.Prefix
	for _, prefix := range current {
		for _, original := range selected {
			if prefix.Addr() == original.Addr() {
				prefixes = append(prefixes, prefix)
				break
			}
		}
	}
	return prefixes
}

// Close releases the socket and interrupts any pending receive.
func (s *Socket) Close() error { return s.conn.Close() }

// Serve owns the Machine, unicasts to each request's original
// source address/port from the proxy LAN address and server port, and joins its
// cancellation callback. No authentication or remote allocation is performed.
func (s *Socket) Serve(ctx context.Context, m *Machine) error {
	stopped := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { s.Close(); close(stopped) })
	defer func() {
		if !stop() {
			<-stopped
		}
	}()
	defer s.Close()
	elapsed := s.elapsed
	if elapsed == nil {
		start := time.Now()
		elapsed = func() time.Duration { return time.Since(start) }
	}
	b := make([]byte, MaxDatagram+1)
	defer func() { s.summarize(m, elapsed(), true) }()
	for {
		now := elapsed()
		s.summarize(m, now, false)
		if err := s.packet.SetReadDeadline(time.Now().Add(refusalInterval - max(0, now-s.summaryLast))); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return s.deliveryError("read_deadline_failed", err)
		}
		n, cm, source, err := s.packet.ReadFrom(b)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// A truncated datagram or ancillary buffer cannot authorize a reply.
			// x/net reports Windows WSAEMSGSIZE and truncation flags this way.
			if errors.Is(err, io.ErrShortBuffer) {
				m.refuse(refusalMetadata)
				continue
			}
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				continue
			}
			return err
		}
		response, local := s.received(m, b[:n], cm, source, elapsed())
		if response == nil {
			continue
		}
		if err := s.deliver(response, local, cm.IfIndex, source); err != nil {
			return err
		}
	}
}

func (s *Socket) received(m *Machine, b []byte, cm *ipv4.ControlMessage, source net.Addr, now time.Duration) ([]byte, netip.Addr) {
	if len(b) > MaxDatagram {
		m.refuse(refusalOversize)
		return nil, netip.Addr{}
	}
	if cm == nil {
		m.refuse(refusalMetadata)
		return nil, netip.Addr{}
	}
	addr, ok := source.(*net.UDPAddr)
	if !ok || addr.Port == 0 {
		m.refuse(refusalMetadata)
		return nil, netip.Addr{}
	}
	requester, ok := netip.AddrFromSlice(addr.IP)
	if !ok {
		m.refuse(refusalMetadata)
		return nil, netip.Addr{}
	}
	destination, ok := netip.AddrFromSlice(cm.Dst)
	if !ok {
		m.refuse(refusalMetadata)
		return nil, netip.Addr{}
	}
	return s.response(m, b, now, cm.IfIndex, requester.Unmap(), destination.Unmap())
}

func (s *Socket) deliveryError(rule string, err error) error {
	if s.Logger != nil {
		s.Logger.Warn("lan_discovery_delivery_failed", "rule", rule)
	}
	return fmt.Errorf("LAN discovery: %s: %w", rule, err)
}

func (s *Socket) deliver(response []byte, local netip.Addr, index int, source net.Addr) error {
	if err := s.packet.SetWriteDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		return s.deliveryError("reply_deadline_failed", err)
	}
	// Source pinning keeps the app's subsequent TCP dial on the selected link.
	n, err := s.packet.WriteTo(response, &ipv4.ControlMessage{Src: net.IP(local.AsSlice()), IfIndex: index}, source)
	if err != nil {
		return s.deliveryError("reply_write_failed", err)
	}
	if n != len(response) {
		return s.deliveryError("reply_short_write", errors.New("short datagram write"))
	}
	return nil
}

// response is the receive path's admission and reply decision. Keeping it
// separate from socket I/O permits deterministic work/allocation regressions.
// Live-prefix filtering, rate limits and source disambiguation are local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/lan-discovery/app-scan.md#reply
func (s *Socket) response(m *Machine, b []byte, now time.Duration, index int, requester, destination netip.Addr) (response []byte, source netip.Addr) {
	r, ok := m.prepare(b, now)
	if !ok {
		return nil, netip.Addr{}
	}
	// Admission failures preserve reply tokens but must not allow unlimited
	// live interface dumps. Exhaustion drops the scan without cached admission.
	if !s.takeInspection(now) {
		m.refuse(refusalInspection)
		return nil, netip.Addr{}
	}
	prefixes := s.currentPrefixes(index)
	if len(prefixes) == 0 {
		m.refuse(refusalInterface)
		return nil, netip.Addr{}
	}
	local, ambiguous := selectReplySource(prefixes, requester, destination)
	if !local.IsValid() {
		if ambiguous {
			m.refuse(refusalAlias)
		} else {
			m.refuse(refusalSource)
		}
		return nil, netip.Addr{}
	}
	return m.reply(r, now), local
}

// onLink is a local admission policy: discovery must not disclose identity to
// routed requesters. /31 point-to-point and /32 host prefixes have no broadcast
// or reserved network address; wider IPv4 prefixes exclude both boundaries.
func onLink(prefix netip.Prefix, requester netip.Addr) bool {
	if !requester.Is4() || (!requester.IsGlobalUnicast() && !requester.IsLoopback()) || !prefix.Contains(requester) {
		return false
	}
	return prefix.Bits() >= 31 || (requester != prefix.Masked().Addr() && requester != broadcast(prefix))
}

func broadcast(prefix netip.Prefix) netip.Addr {
	address := prefix.Masked().Addr().As4()
	for bit := prefix.Bits(); bit < 32; bit++ {
		address[bit/8] |= 1 << (7 - bit%8)
	}
	return netip.AddrFrom4(address)
}

// replySource keeps the app's subsequent TCP dial on the requester's link.
// Unicast must target the selected address; directed broadcast must target its
// prefix. Limited broadcast can use a unique on-link address. If aliases make
// that choice ambiguous, require a unicast scan or an explicit bind.
func replySource(prefixes []netip.Prefix, requester, destination netip.Addr) netip.Addr {
	selected, _ := selectReplySource(prefixes, requester, destination)
	return selected
}

func selectReplySource(prefixes []netip.Prefix, requester, destination netip.Addr) (netip.Addr, bool) {
	var selected netip.Addr
	for _, prefix := range prefixes {
		if !onLink(prefix, requester) {
			continue
		}
		if destination != prefix.Addr() && destination != netip.AddrFrom4([4]byte{255, 255, 255, 255}) &&
			(prefix.Bits() >= 31 || destination != broadcast(prefix)) {
			continue
		}
		if selected.IsValid() && selected != prefix.Addr() {
			return netip.Addr{}, true
		}
		selected = prefix.Addr()
	}
	return selected, false
}
