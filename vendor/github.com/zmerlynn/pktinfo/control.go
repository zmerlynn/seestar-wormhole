// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pktinfo

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"runtime"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// Windows ABI values, checked against golang.org/x/sys/windows in tests.
const (
	levelIP          = 0  // IPPROTO_IP
	levelIPv6        = 41 // IPPROTO_IPV6
	typePacketInfo   = 19 // IP_PKTINFO and IPV6_PKTINFO
	sizeofInPktinfo  = 8  // IN_PKTINFO: IN_ADDR, ULONG interface index
	sizeofIn6Pktinfo = 20 // IN6_PKTINFO: IN6_ADDR, ULONG interface index
	msgTrunc         = 0x100
	msgCtrunc        = 0x200
)

var (
	errInvalidControl  = errors.New("invalid control message")
	errSourceRequired  = errors.New("source address required with interface index on Windows")
	errUnspecifiedSrc  = errors.New("unspecified source address not supported on Windows")
	errInvalidConnType = errors.New("invalid conn type")
	errNotImplemented  = errors.New("not implemented on " + runtime.GOOS + "/" + runtime.GOARCH)
)

// validateIPv4 checks IPv4 send controls for Windows.
func validateIPv4(cm *ipv4.ControlMessage) error {
	if cm == nil {
		return nil
	}
	// Like Marshal on other platforms, ignore TTL, which is receive-only,
	// a non-IPv4 source and a non-positive interface index.
	if cm.IfIndex > 0 && uint64(cm.IfIndex) > uint64(^uint32(0)) {
		return errInvalidControl
	}
	// Windows does not choose a source for a zero IN_PKTINFO address,
	// so an interface index alone cannot select the outgoing interface.
	if cm.IfIndex > 0 && (cm.Src.To4() == nil || cm.Src.IsUnspecified()) {
		return errSourceRequired
	}
	return nil
}

// validateIPv6 checks IPv6 send controls for Windows. Unsupported controls
// return errNotImplemented rather than sending a datagram without them.
func validateIPv6(cm *ipv6.ControlMessage) error {
	if cm == nil {
		return nil
	}
	if cm.TrafficClass > 0 || cm.HopLimit > 0 || cm.NextHop.To16() != nil && cm.NextHop.To4() == nil {
		return errNotImplemented
	}
	// Like Marshal, ignore a non-IPv6 source and a non-positive
	// interface index.
	if cm.IfIndex > 0 && uint64(cm.IfIndex) > uint64(^uint32(0)) {
		return errInvalidControl
	}
	// Windows does not choose a source for a zero IN6_PKTINFO address.
	// It sends from the unspecified address, which peers cannot reply to.
	src := ipv6Source(cm)
	if cm.IfIndex > 0 && (src == nil || src.IsUnspecified()) {
		return errSourceRequired
	}
	if src != nil && src.IsUnspecified() {
		return errUnspecifiedSrc
	}
	return nil
}

func ipv6Source(cm *ipv6.ControlMessage) net.IP {
	if src := cm.Src.To16(); src != nil && src.To4() == nil {
		return src
	}
	return nil
}

// marshalIPv4 returns the IP_PKTINFO record for validated controls, if any.
func marshalIPv4(a layout, cm *ipv4.ControlMessage) []byte {
	if cm == nil || cm.Src.To4() == nil && cm.IfIndex <= 0 {
		return nil
	}
	var pi [sizeofInPktinfo]byte
	if ip := cm.Src.To4(); ip != nil {
		copy(pi[:4], ip)
	}
	if cm.IfIndex > 0 {
		binary.LittleEndian.PutUint32(pi[4:], uint32(cm.IfIndex))
	}
	return a.marshal(levelIP, typePacketInfo, pi[:])
}

// marshalIPv6 returns the IPV6_PKTINFO record for validated controls, if any.
func marshalIPv6(a layout, cm *ipv6.ControlMessage) []byte {
	if cm == nil {
		return nil
	}
	src := ipv6Source(cm)
	if src == nil && cm.IfIndex <= 0 {
		return nil
	}
	var pi [sizeofIn6Pktinfo]byte
	copy(pi[:16], src)
	if cm.IfIndex > 0 {
		binary.LittleEndian.PutUint32(pi[16:], uint32(cm.IfIndex))
	}
	return a.marshal(levelIPv6, typePacketInfo, pi[:])
}

// checkControls returns the received controls, or io.ErrShortBuffer if the
// payload or controls were truncated. It returns nil if there are none.
func checkControls(oob []byte, nn, flags int) ([]byte, error) {
	if flags&(msgCtrunc|msgTrunc) != 0 || nn < 0 || nn > len(oob) {
		return nil, io.ErrShortBuffer
	}
	if nn == 0 {
		return nil, nil
	}
	return oob[:nn], nil
}

// parseIPv4 parses received IPv4 controls. Short and unknown records are
// ignored.
func parseIPv4(a layout, oob []byte, nn, flags int) (*ipv4.ControlMessage, error) {
	b, err := checkControls(oob, nn, flags)
	if b == nil {
		return nil, err
	}
	cm := new(ipv4.ControlMessage)
	err = a.parse(b, func(level, typ int, data []byte) {
		if level == levelIP && typ == typePacketInfo && len(data) >= sizeofInPktinfo {
			cm.Dst = net.IPv4(data[0], data[1], data[2], data[3]).To4()
			cm.IfIndex = int(binary.LittleEndian.Uint32(data[4:]))
		}
	})
	if err != nil {
		return nil, err
	}
	return cm, nil
}

// parseIPv6 parses received IPv6 controls. Short and unknown records are
// ignored.
func parseIPv6(a layout, oob []byte, nn, flags int) (*ipv6.ControlMessage, error) {
	b, err := checkControls(oob, nn, flags)
	if b == nil {
		return nil, err
	}
	cm := new(ipv6.ControlMessage)
	err = a.parse(b, func(level, typ int, data []byte) {
		if level == levelIPv6 && typ == typePacketInfo && len(data) >= sizeofIn6Pktinfo {
			cm.Dst = make(net.IP, net.IPv6len)
			copy(cm.Dst, data[:16])
			cm.IfIndex = int(binary.LittleEndian.Uint32(data[16:]))
		}
	})
	if err != nil {
		return nil, err
	}
	return cm, nil
}
