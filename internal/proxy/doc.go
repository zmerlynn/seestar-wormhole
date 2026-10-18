// Package proxy composes a single device session and exposes bounded net.Conn
// streams plus local TCP forwarding. Protocol machines run on the session owner;
// cancellable edge workers perform I/O. See DESIGN.md for the internal session stream interface.
//
// Wire reference: https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/seestar-profile.md
package proxy
