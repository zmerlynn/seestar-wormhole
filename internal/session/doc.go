// Package session owns one device session through a pure, single-owner Step core.
// It applies lifecycle, retry and resource-budget policy without I/O, timers,
// goroutines or protocol dependencies. sessionedge composes consumer-defined layer interfaces;
// callers must create exactly one owner per device (there is no process registry).
//
// Wire reference: https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/state-timers.md
package session
