// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pktinfo

import (
	"errors"
	"io"
	"net"
	"os"
	"sync"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/windows"
)

// sysConn holds the control state of a *net.UDPConn.
type sysConn struct {
	udp   *net.UDPConn
	mu    sync.Mutex
	flags uint // enabled control flags
}

func newSysConn(c net.PacketConn) *sysConn {
	// Other connections, including wrapped UDP connections, use x/net.
	udp, ok := c.(*net.UDPConn)
	if !ok {
		return nil
	}
	return &sysConn{udp: udp}
}

// setFlags changes the enabled flags. The packet information socket option
// stays enabled while any flag in info is enabled, including when flags are
// changed independently.
func (c *sysConn) setFlags(cf uint, on bool, level, name int, info uint) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	flags := c.flags
	if on {
		flags |= cf
	} else {
		flags &^= cf
	}
	v := 0
	if flags&info != 0 {
		v = 1
	}
	rc, err := c.udp.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) {
		serr = windows.SetsockoptInt(windows.Handle(fd), level, name, v)
	}); err != nil {
		return err
	}
	if serr != nil {
		return os.NewSyscallError("setsockopt", serr)
	}
	c.flags = flags
	return nil
}

// readMsgUDP reads a datagram and its controls. UDPConn uses the runtime
// poller and overlapped WSARecvMsg. In particular, do not perform blocking
// message I/O in a syscall.RawConn callback.
func readMsgUDP(udp *net.UDPConn, b, oob []byte) (n, nn, flags int, from *net.UDPAddr, err error) {
	n, nn, flags, from, err = udp.ReadMsgUDP(b, oob)
	if err != nil {
		if oe, ok := err.(*net.OpError); ok && errors.Is(err, windows.WSAEMSGSIZE) {
			e := *oe
			e.Err = errors.Join(io.ErrShortBuffer, oe.Err)
			err = &e
		}
	}
	return
}

func readError(udp *net.UDPConn, from net.Addr, err error) error {
	return &net.OpError{Op: "read", Net: "udp", Source: udp.LocalAddr(), Addr: from, Err: err}
}

func (c *sysConn) writeError(dst net.Addr, err error) error {
	return &net.OpError{Op: "write", Net: c.udp.LocalAddr().Network(), Source: c.udp.LocalAddr(), Addr: dst, Err: err}
}

func (c *sysConn) writeMsgUDP(b, oob []byte, dst net.Addr) (int, error) {
	var to *net.UDPAddr
	if dst != nil {
		var ok bool
		if to, ok = dst.(*net.UDPAddr); !ok {
			return 0, c.writeError(dst, errInvalidConnType)
		}
	}
	// WriteMsgUDP rejects any destination on a connected socket. As with
	// sendmsg on other platforms, allow the connected peer's address.
	if ra, ok := c.udp.RemoteAddr().(*net.UDPAddr); ok && to != nil &&
		to.IP.Equal(ra.IP) && to.Port == ra.Port && to.Zone == ra.Zone {
		to = nil
	}
	n, _, err := c.udp.WriteMsgUDP(b, oob, to)
	if err == nil {
		// WriteMsgUDP can report zero bytes for a send that completes
		// immediately (go.dev/issue/82335). UDP sends are atomic, so a nil
		// error means the whole datagram was accepted. Remove this once the
		// minimum supported Go version includes the fix.
		n = len(b)
	}
	return n, err
}

const ipv4Info = uint(ipv4.FlagDst | ipv4.FlagInterface)

// SetControlMessage is [ipv4.PacketConn.SetControlMessage], with FlagDst
// and FlagInterface for a *net.UDPConn.
func (c *IPv4PacketConn) SetControlMessage(cf ipv4.ControlFlags, on bool) error {
	if c.sys == nil {
		return c.PacketConn.SetControlMessage(cf, on)
	}
	if uint(cf)&^ipv4Info != 0 {
		return errNotImplemented
	}
	return c.sys.setFlags(uint(cf), on, windows.IPPROTO_IP, windows.IP_PKTINFO, ipv4Info)
}

// ReadFrom is [ipv4.PacketConn.ReadFrom], with packet information controls
// for a *net.UDPConn.
func (c *IPv4PacketConn) ReadFrom(b []byte) (n int, cm *ipv4.ControlMessage, src net.Addr, err error) {
	if c.sys == nil {
		return c.PacketConn.ReadFrom(b)
	}
	// Always provide space for packet information, which another goroutine
	// can enable while this read is waiting.
	return readIPv4(c.sys.udp, b, make([]byte, native.space(sizeofInPktinfo)))
}

func readIPv4(udp *net.UDPConn, b, oob []byte) (int, *ipv4.ControlMessage, net.Addr, error) {
	n, nn, flags, from, err := readMsgUDP(udp, b, oob)
	if err != nil {
		return 0, nil, nil, err
	}
	cm, err := parseIPv4(native, oob, nn, flags)
	if err != nil {
		return 0, nil, nil, readError(udp, from, err)
	}
	if cm != nil {
		cm.Src = from.IP.To4()
	}
	return n, cm, from, nil
}

// WriteTo is [ipv4.PacketConn.WriteTo], with the Src and IfIndex controls
// for a *net.UDPConn.
func (c *IPv4PacketConn) WriteTo(b []byte, cm *ipv4.ControlMessage, dst net.Addr) (n int, err error) {
	if c.sys == nil {
		return c.PacketConn.WriteTo(b, cm, dst)
	}
	if err := validateIPv4(cm); err != nil {
		return 0, c.sys.writeError(dst, err)
	}
	return c.sys.writeMsgUDP(b, marshalIPv4(native, cm), dst)
}

const ipv6Info = uint(ipv6.FlagDst | ipv6.FlagInterface)

// SetControlMessage is [ipv6.PacketConn.SetControlMessage], with FlagSrc,
// FlagDst and FlagInterface for a *net.UDPConn.
func (c *IPv6PacketConn) SetControlMessage(cf ipv6.ControlFlags, on bool) error {
	if c.sys == nil {
		return c.PacketConn.SetControlMessage(cf, on)
	}
	// FlagSrc needs no socket option: as on other platforms, the source is
	// taken from the datagram's address when packet info is received.
	if uint(cf)&^(uint(ipv6.FlagSrc)|ipv6Info) != 0 {
		return errNotImplemented
	}
	return c.sys.setFlags(uint(cf), on, windows.IPPROTO_IPV6, windows.IPV6_PKTINFO, ipv6Info)
}

// ReadFrom is [ipv6.PacketConn.ReadFrom], with packet information controls
// for a *net.UDPConn.
func (c *IPv6PacketConn) ReadFrom(b []byte) (n int, cm *ipv6.ControlMessage, src net.Addr, err error) {
	if c.sys == nil {
		return c.PacketConn.ReadFrom(b)
	}
	// Always provide space for packet information, which another goroutine
	// can enable while this read is waiting.
	return readIPv6(c.sys.udp, b, make([]byte, native.space(sizeofIn6Pktinfo)))
}

func readIPv6(udp *net.UDPConn, b, oob []byte) (int, *ipv6.ControlMessage, net.Addr, error) {
	n, nn, flags, from, err := readMsgUDP(udp, b, oob)
	if err != nil {
		return 0, nil, nil, err
	}
	cm, err := parseIPv6(native, oob, nn, flags)
	if err != nil {
		return 0, nil, nil, readError(udp, from, err)
	}
	if cm != nil {
		cm.Src = from.IP.To16()
	}
	return n, cm, from, nil
}

// WriteTo is [ipv6.PacketConn.WriteTo], with the Src and IfIndex controls
// for a *net.UDPConn.
func (c *IPv6PacketConn) WriteTo(b []byte, cm *ipv6.ControlMessage, dst net.Addr) (n int, err error) {
	if c.sys == nil {
		return c.PacketConn.WriteTo(b, cm, dst)
	}
	if err := validateIPv6(cm); err != nil {
		return 0, c.sys.writeError(dst, err)
	}
	return c.sys.writeMsgUDP(b, marshalIPv6(native, cm), dst)
}
