// Package rdt encodes bounded packets and implements a pure, single-owner
// reliable stream above authenticated, packet-preserving DTLS application data.
// Machine.Step consumes packets and monotonic events and returns datagrams,
// ordered bytes, queue/credit snapshots and a single replaceable timer. The edge
// owns authentication, packet I/O, scheduling and bounded action/inbox storage.
// See this package's README.md for local policy and integration limits.
//
// Wire reference: https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/rdt.md
package rdt
