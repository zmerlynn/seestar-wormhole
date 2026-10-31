// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pktinfo

import (
	"encoding/binary"
	"errors"
	"unsafe"
)

// A layout describes Windows ancillary data (WSACMSGHDR records) for one
// SIZE_T size. Each record is a SIZE_T length followed by INT level and type,
// all little-endian, and both the header and the data are aligned to SIZE_T
// boundaries. The records are encoded without unsafe conversions so that
// both layouts can be tested and fuzzed on any platform.
type layout int

// native is the layout of the running platform.
const native = layout(unsafe.Sizeof(uintptr(0)))

var errInvalidLength = errors.New("invalid message length")

func (a layout) headerLen() int { return int(a) + 8 }

func (a layout) space(dataLen int) int {
	return a.headerLen() + (dataLen+int(a)-1)&^(int(a)-1)
}

// marshal returns a record for data, padded to its full space.
func (a layout) marshal(level, typ int, data []byte) []byte {
	b := make([]byte, a.space(len(data)))
	l := uint64(a.headerLen() + len(data))
	if a == 4 {
		binary.LittleEndian.PutUint32(b, uint32(l))
	} else {
		binary.LittleEndian.PutUint64(b, l)
	}
	binary.LittleEndian.PutUint32(b[a:], uint32(int32(level)))
	binary.LittleEndian.PutUint32(b[a+4:], uint32(int32(typ)))
	copy(b[a.headerLen():], data)
	return b
}

// parse calls f for each record in b. Trailing bytes too short for a header
// are ignored, and the final record may omit some or all of its padding.
func (a layout) parse(b []byte, f func(level, typ int, data []byte)) error {
	for len(b) >= a.headerLen() {
		var l uint64
		if a == 4 {
			l = uint64(binary.LittleEndian.Uint32(b))
		} else {
			l = binary.LittleEndian.Uint64(b)
		}
		if l < uint64(a.headerLen()) || l > uint64(len(b)) {
			return errInvalidLength
		}
		level := int(int32(binary.LittleEndian.Uint32(b[a:])))
		typ := int(int32(binary.LittleEndian.Uint32(b[a+4:])))
		f(level, typ, b[a.headerLen():l])
		if space := a.space(int(l) - a.headerLen()); space <= len(b) {
			b = b[space:]
		} else {
			b = nil
		}
	}
	return nil
}
