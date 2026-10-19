// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package socket

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows aligns both the header and the data to SIZE_T boundaries.
func controlHeaderLen() int             { return int(unsafe.Sizeof(windows.WSACMSGHDR{})) }
func controlMessageLen(dataLen int) int { return controlHeaderLen() + dataLen }
func controlMessageSpace(dataLen int) int {
	const align = int(unsafe.Sizeof(uintptr(0)))
	return controlHeaderLen() + (dataLen+align-1)&^(align-1)
}

type cmsghdr windows.WSACMSGHDR

func (h *cmsghdr) len() int { return int(h.Len) }
func (h *cmsghdr) lvl() int { return int(h.Level) }
func (h *cmsghdr) typ() int { return int(h.Type) }
func (h *cmsghdr) set(l, lvl, typ int) {
	h.Len = uintptr(l)
	h.Level = int32(lvl)
	h.Type = int32(typ)
}
