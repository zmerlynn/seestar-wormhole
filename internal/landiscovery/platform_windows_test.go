//go:build windows

package landiscovery

import "testing"

func TestWindowsLANDiscoveryFailsClosed(t *testing.T) {
	if _, err := Listen("0.0.0.0", 0); err == nil {
		t.Fatal("discovery started without ingress metadata")
	}
}
