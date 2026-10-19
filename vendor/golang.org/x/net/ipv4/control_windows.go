// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ipv4

import (
	"net"
	"unsafe"

	"golang.org/x/net/internal/socket"
	"golang.org/x/sys/windows"
)

const sizeofInetPktinfo = int(unsafe.Sizeof(windows.IN_PKTINFO{}))

func setControlMessage(c *socket.Conn, opt *rawOpt, cf ControlFlags, on bool) error {
	if cf & ^(FlagDst|FlagInterface) != 0 {
		return errNotImplemented
	}
	opt.Lock()
	defer opt.Unlock()
	flags := opt.cflags
	if on {
		flags |= cf
	} else {
		flags &^= cf
	}
	// Both controls share IP_PKTINFO. Keep it enabled until the last control
	// is disabled, including when flags are changed independently.
	if err := sockOpts[ssoPacketInfo].SetInt(c, boolint(flags != 0)); err != nil {
		return err
	}
	opt.cflags = flags
	return nil
}

func marshalPacketInfo(b []byte, cm *ControlMessage) []byte {
	m := socket.ControlMessage(b)
	m.MarshalHeader(windows.IPPROTO_IP, windows.IP_PKTINFO, sizeofInetPktinfo)
	pi := (*windows.IN_PKTINFO)(unsafe.Pointer(&m.Data(sizeofInetPktinfo)[0]))
	if cm != nil {
		if ip := cm.Src.To4(); ip != nil {
			copy(pi.Addr[:], ip)
		}
		if cm.IfIndex > 0 {
			pi.Ifindex = uint32(cm.IfIndex)
		}
	}
	return m.Next(sizeofInetPktinfo)
}

func parsePacketInfo(cm *ControlMessage, b []byte) {
	pi := (*windows.IN_PKTINFO)(unsafe.Pointer(&b[0]))
	cm.Dst = net.IPv4(pi.Addr[0], pi.Addr[1], pi.Addr[2], pi.Addr[3]).To4()
	cm.IfIndex = int(pi.Ifindex)
}
