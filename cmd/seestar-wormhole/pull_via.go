package main

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

func normalizeVia(value string) (string, error) {
	invalid := &argumentError{Option: "--via", Rule: "use host:port with a numeric port from 1 through 65535; bracket IPv6"}
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || strings.ContainsAny(host, "/\\@?# \t\r\n") {
		return "", invalid
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return "", invalid
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.String()
	} else {
		if strings.ContainsAny(host, ":[]") {
			return "", invalid
		}
		host = strings.ToLower(strings.TrimSuffix(host, "."))
		if host == "" {
			return "", invalid
		}
	}
	return net.JoinHostPort(host, strconv.FormatUint(number, 10)), nil
}

// viaDialer owns only TCP connections to the ambient proxy, never its session.
type viaDialer struct{ endpoint string }

func (d viaDialer) DialPort(ctx context.Context, _ uint16) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", d.endpoint)
}
