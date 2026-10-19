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

// ReadFrom reads a UDP payload and its destination and interface controls.
// Truncated payloads or controls return an error wrapping io.ErrShortBuffer,
// without a partial control message.
func (c *payloadHandler) ReadFrom(b []byte) (int, *ControlMessage, net.Addr, error) {
	if !c.ok() {
		return 0, nil, nil, errInvalidConn
	}
	udp, ok := c.PacketConn.(*net.UDPConn)
	if !ok {
		return 0, nil, nil, errNotImplemented
	}
	c.rawOpt.RLock()
	oob := NewControlMessage(c.rawOpt.cflags)
	c.rawOpt.RUnlock()
	// UDPConn uses the runtime poller and overlapped WSARecvMsg. In particular,
	// do not perform blocking message I/O in a syscall.RawConn callback.
	n, nn, flags, from, err := udp.ReadMsgUDP(b, oob)
	cm, err := parseControlMessage(oob, nn, flags, err)
	if err != nil {
		return 0, nil, nil, &net.OpError{Op: "read", Net: "udp", Source: udp.LocalAddr(), Err: err}
	}
	return n, cm, from, nil
}

func parseControlMessage(oob []byte, nn, flags int, err error) (*ControlMessage, error) {
	if errors.Is(err, windows.WSAEMSGSIZE) {
		return nil, errors.Join(io.ErrShortBuffer, err)
	}
	if err != nil {
		return nil, err
	}
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

// WriteTo writes a UDP payload with optional source and interface controls.
func (c *payloadHandler) WriteTo(b []byte, cm *ControlMessage, dst net.Addr) (int, error) {
	if !c.ok() {
		return 0, errInvalidConn
	}
	udp, ok := c.PacketConn.(*net.UDPConn)
	if !ok {
		return 0, errNotImplemented
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
		// internal/poll.FD.WriteMsg passes a nil byte-count pointer to
		// WSASendMsg, so synchronous success can report zero bytes. UDP sends
		// are atomic: a nil error means the whole datagram was accepted.
		// Remove this workaround once the standard library reports the count.
		n = len(b)
	}
	return n, err
}
