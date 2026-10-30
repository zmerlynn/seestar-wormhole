// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !windows

package pktinfo

import "net"

// sysConn is unused: other platforms use the x/net connections unchanged.
type sysConn struct{}

func newSysConn(net.PacketConn) *sysConn { return nil }
