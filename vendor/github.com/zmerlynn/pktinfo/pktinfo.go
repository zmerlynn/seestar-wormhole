// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package pktinfo provides golang.org/x/net/ipv4 and ipv6 packet
// connections whose UDP packet information controls also work on Windows
// (go.dev/issue/7175).
//
// On platforms other than Windows, the connections are the x/net
// connections. On Windows, SetControlMessage, ReadFrom and WriteTo support
// these controls for a *net.UDPConn:
//
//   - IPv4: FlagDst and FlagInterface on receive; Src and IfIndex on send.
//   - IPv6: FlagSrc, FlagDst and FlagInterface on receive; Src and IfIndex
//     on send.
//
// Other controls and connections behave as they do in x/net on Windows,
// except as follows. Unsupported flags, and IPv6 TrafficClass, HopLimit and
// NextHop on send, return an error rather than being ignored. Windows does
// not choose a source address for a packet information control with only
// an interface index, so a send with IfIndex requires a specified Src.
// Truncated payloads or controls return an error wrapping io.ErrShortBuffer.
// A dual-stack IPv6 socket receives no packet information for IPv4
// datagrams, so use separate "udp4" and "udp6" sockets. A connected socket
// accepts its peer's address or a nil address as the destination.
//
// All other methods, including batch I/O and multicast, are the x/net
// methods.
package pktinfo

import (
	"net"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// An IPv4PacketConn is an [ipv4.PacketConn] with UDP packet information
// controls on Windows.
type IPv4PacketConn struct {
	*ipv4.PacketConn
	sys *sysConn // nil unless the controls need platform support
}

// NewIPv4PacketConn returns a new IPv4PacketConn using c as its underlying
// transport.
func NewIPv4PacketConn(c net.PacketConn) *IPv4PacketConn {
	return &IPv4PacketConn{PacketConn: ipv4.NewPacketConn(c), sys: newSysConn(c)}
}

// An IPv6PacketConn is an [ipv6.PacketConn] with UDP packet information
// controls on Windows.
type IPv6PacketConn struct {
	*ipv6.PacketConn
	sys *sysConn // nil unless the controls need platform support
}

// NewIPv6PacketConn returns a new IPv6PacketConn using c as its underlying
// transport.
func NewIPv6PacketConn(c net.PacketConn) *IPv6PacketConn {
	return &IPv6PacketConn{PacketConn: ipv6.NewPacketConn(c), sys: newSysConn(c)}
}
