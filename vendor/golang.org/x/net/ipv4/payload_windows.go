// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ipv4

import (
	"errors"
	"io"
	"net"

	"golang.org/x/sys/windows"
)

// ReadFrom reads a payload and, for UDP, its destination and interface
// controls. Other connections are read without controls, as before UDP
// controls were supported. Truncated payloads or controls return an error
// wrapping io.ErrShortBuffer, without a partial control message.
func (c *payloadHandler) ReadFrom(b []byte) (int, *ControlMessage, net.Addr, error) {
	if !c.ok() {
		return 0, nil, nil, errInvalidConn
	}
	udp, ok := c.PacketConn.(*net.UDPConn)
	if !ok {
		n, src, err := c.PacketConn.ReadFrom(b)
		if err != nil {
			return 0, nil, nil, err
		}
		return n, nil, src, nil
	}
	c.rawOpt.RLock()
	oob := NewControlMessage(c.rawOpt.cflags)
	c.rawOpt.RUnlock()
	return readMsgUDP(udp, b, oob)
}

// readMsgUDP reads a datagram and parses the controls received in oob.
// UDPConn uses the runtime poller and overlapped WSARecvMsg. In particular,
// do not perform blocking message I/O in a syscall.RawConn callback.
func readMsgUDP(udp *net.UDPConn, b, oob []byte) (int, *ControlMessage, net.Addr, error) {
	n, nn, flags, from, err := udp.ReadMsgUDP(b, oob)
	if err != nil {
		if oe, ok := err.(*net.OpError); ok && errors.Is(err, windows.WSAEMSGSIZE) {
			e := *oe
			e.Err = errors.Join(io.ErrShortBuffer, oe.Err)
			err = &e
		}
		return 0, nil, nil, err
	}
	cm, err := parseControlMessage(oob, nn, flags)
	if err != nil {
		return 0, nil, nil, &net.OpError{Op: "read", Net: "udp", Source: udp.LocalAddr(), Addr: from, Err: err}
	}
	return n, cm, from, nil
}

func parseControlMessage(oob []byte, nn, flags int) (*ControlMessage, error) {
	if flags&(windows.MSG_CTRUNC|windows.MSG_TRUNC) != 0 {
		return nil, io.ErrShortBuffer
	}
	if nn < 0 || nn > len(oob) {
		return nil, io.ErrShortBuffer
	}
	if nn == 0 {
		return nil, nil
	}
	cm := new(ControlMessage)
	if err := cm.Parse(oob[:nn]); err != nil {
		return nil, err
	}
	return cm, nil
}

// WriteTo writes a payload and, for UDP, optional source and interface
// controls. Other connections ignore cm, as before UDP controls were
// supported.
func (c *payloadHandler) WriteTo(b []byte, cm *ControlMessage, dst net.Addr) (int, error) {
	if !c.ok() {
		return 0, errInvalidConn
	}
	udp, ok := c.PacketConn.(*net.UDPConn)
	if !ok {
		if dst == nil {
			return 0, errMissingAddress
		}
		return c.PacketConn.WriteTo(b, dst)
	}
	var to *net.UDPAddr
	if dst != nil {
		to, ok = dst.(*net.UDPAddr)
		if !ok {
			return 0, errInvalidConnType
		}
	}
	if err := cm.validateWindows(); err != nil {
		return 0, err
	}

	n, _, err := udp.WriteMsgUDP(b, cm.Marshal(), to)
	if err == nil {
		// WriteMsgUDP can report zero bytes for a send that completes
		// immediately (go.dev/issue/82335). UDP sends are atomic, so a nil
		// error means the whole datagram was accepted. Remove this once the
		// minimum supported Go version includes the fix.
		n = len(b)
	}
	return n, err
}
