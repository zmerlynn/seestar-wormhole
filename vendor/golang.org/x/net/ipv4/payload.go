// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ipv4

import (
	"net"

	"golang.org/x/net/internal/socket"
)

// BUG(mikio): On Windows, PacketConn control messages are supported only
// for UDP. SetControlMessage supports FlagDst and FlagInterface; WriteTo
// supports Src and IfIndex. When IfIndex is positive, WriteTo requires a
// non-unspecified IPv4 Src and otherwise returns an error before sending:
// "source address required with interface index on Windows".
// IPConn, receive TTL and receive source controls are not implemented.

// A payloadHandler represents the IPv4 datagram payload handler.
type payloadHandler struct {
	net.PacketConn
	*socket.Conn
	rawOpt
}

func (c *payloadHandler) ok() bool { return c != nil && c.PacketConn != nil && c.Conn != nil }
