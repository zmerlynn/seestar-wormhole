// Package interfaceio supplies the OS interface operations used by discovery.
package interfaceio

import "net"

// These seams let socket and command tests use synthetic adapters. Tests that
// replace them must restore them and must not run in parallel.
var (
	Interfaces       = net.Interfaces
	InterfaceByIndex = net.InterfaceByIndex
	Addrs            = (*net.Interface).Addrs
)
