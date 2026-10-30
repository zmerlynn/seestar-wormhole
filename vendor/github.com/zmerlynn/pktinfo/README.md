# pktinfo

[![Go Reference](https://pkg.go.dev/badge/github.com/zmerlynn/pktinfo.svg)](https://pkg.go.dev/github.com/zmerlynn/pktinfo)

Package pktinfo wraps the `golang.org/x/net/ipv4` and `golang.org/x/net/ipv6`
packet connections so that UDP packet information controls (the destination
address and interface of a received datagram, and the source address and
interface of a sent one) also work on Windows. x/net does not support them
there yet ([go.dev/issue/7175](https://go.dev/issue/7175)).

```go
c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 4720})
if err != nil {
	return err
}
p := pktinfo.NewIPv4PacketConn(c)
if err := p.SetControlMessage(ipv4.FlagDst|ipv4.FlagInterface, true); err != nil {
	return err
}
n, cm, from, err := p.ReadFrom(buf)
if err != nil {
	return err
}
// Reply from the address and interface the request arrived on.
_, err = p.WriteTo(reply, &ipv4.ControlMessage{Src: cm.Dst, IfIndex: cm.IfIndex}, from)
```

On platforms other than Windows the connections are the x/net connections
unchanged. On Windows, for a `*net.UDPConn`:

| Family | Receive                               | Send            |
|--------|---------------------------------------|-----------------|
| IPv4   | `FlagDst`, `FlagInterface`            | `Src`, `IfIndex` |
| IPv6   | `FlagSrc`, `FlagDst`, `FlagInterface` | `Src`, `IfIndex` |

Differences from x/net on other platforms:

- Windows does not choose a source address for a packet information control
  that has only an interface index, so a send with `IfIndex` requires `Src`.
- Unsupported flags, and IPv6 `TrafficClass`, `HopLimit` and `NextHop` on
  send, return an error rather than being ignored.
- Truncated payloads or controls return an error wrapping `io.ErrShortBuffer`.
- A dual-stack IPv6 socket gets no packet information for IPv4 datagrams;
  use separate `udp4` and `udp6` sockets.

All other methods, including multicast and batch I/O, are the x/net methods.

## Upstream

This module is meant to be temporary. Windows support for these controls in
x/net is tracked by [golang/go#7175](https://go.dev/issue/7175) and proposed
in [CL 853705](https://go.dev/cl/853705) and [CL 853706](https://go.dev/cl/853706).
Once x/net supports them, use `ipv4.NewPacketConn` and `ipv6.NewPacketConn`
directly.

## License

BSD-style; see [LICENSE](LICENSE). The code is derived from, and intended to
be contributed back to, golang.org/x/net.
