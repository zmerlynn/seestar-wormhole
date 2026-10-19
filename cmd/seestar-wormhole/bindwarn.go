package main

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"

	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// Both OS operations are injected so startup checks need no live VPN interfaces.
type interfaceSource struct {
	interfaces func() ([]net.Interface, error)
	addrs      func(net.Interface) ([]net.Addr, error)
}

func systemInterfaces() interfaceSource {
	return interfaceSource{net.Interfaces, func(iface net.Interface) ([]net.Addr, error) {
		return iface.Addrs()
	}}
}

// Check TCP forwards together: discovery's filtered wildcard UDP receiver is
// intentional, and interface addresses alone cannot establish its reachability.
func warnWildcardBind(logger *slog.Logger, forwards []proxy.Forward, source interfaceSource) {
	var listeners []string
	includeIPv6 := false
	for _, f := range forwards {
		addr := f.Listener.Addr().(*net.TCPAddr)
		bind := addr.AddrPort().Addr().Unmap()
		if !bind.IsUnspecified() {
			continue
		}
		listeners = append(listeners, fmt.Sprintf("%d:%d", addr.Port, f.Port))
		// net.Listen("tcp", "[::]:port") may be dual stack. Include IPv4
		// conservatively; an actual IPv4-only socket cannot expose IPv6.
		includeIPv6 = includeIPv6 || bind.Is6()
	}
	if len(listeners) == 0 {
		return
	}
	var exposures, inspectionErrors []string
	interfaces, err := source.interfaces()
	if err != nil {
		inspectionErrors = append(inspectionErrors, "interfaces: "+err.Error())
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := source.addrs(iface)
		if err != nil {
			inspectionErrors = append(inspectionErrors, iface.Name+": "+err.Error())
			continue
		}
		for _, raw := range addrs {
			addr, err := interfaceIP(raw.String())
			if err != nil {
				inspectionErrors = append(inspectionErrors, iface.Name+": invalid interface address")
				continue
			}
			ip := addr.Unmap()
			if !includeIPv6 && !ip.Is4() {
				continue
			}
			// IsGlobalUnicast includes CGNAT 100.64/10. Only private LAN
			// addresses are trusted, and private VPN addresses remain exposed.
			if ip.IsGlobalUnicast() && (!ip.IsPrivate() || tunnelInterface(iface)) {
				exposures = append(exposures, iface.Name+"="+addr.String())
			}
		}
	}
	if len(exposures) == 0 && len(inspectionErrors) == 0 {
		return
	}
	attrs := []any{"listeners", listeners, "exposures", exposures,
		"advice", "use an explicit LAN bind for TCP listeners (--bind <LAN IP>)"}
	if len(inspectionErrors) > 0 {
		attrs = append(attrs, "inspection_errors", inspectionErrors)
	}
	logger.Warn("wildcard_bind_warning", attrs...)
}

func interfaceIP(s string) (netip.Addr, error) {
	if prefix, err := netip.ParsePrefix(s); err == nil {
		return prefix.Addr(), nil
	}
	return netip.ParseAddr(s)
}

func tunnelInterface(iface net.Interface) bool {
	if iface.Flags&net.FlagPointToPoint != 0 {
		return true
	}
	// Linux and macOS expose no portable tunnel flag. These conventional
	// names also catch WireGuard/Tailscale interfaces without point-to-point.
	for _, prefix := range []string{"tailscale", "utun", "wg", "tun", "tap", "ipsec", "ppp"} {
		if strings.HasPrefix(iface.Name, prefix) {
			return true
		}
	}
	return false
}
