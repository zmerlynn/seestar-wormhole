//go:build windows

package landiscovery

import "errors"

func platformSupported() error {
	// The vendored x/net/ipv4 Windows backend has no ancillary control codecs.
	// Without ingress interface/destination and pinned reply source, discovery
	// could disclose identity or direct the phone to an unintended interface.
	// Fail startup rather than relax those boundaries or hand-roll a codec.
	return errors.New("LAN discovery: packet metadata unavailable on Windows")
}
