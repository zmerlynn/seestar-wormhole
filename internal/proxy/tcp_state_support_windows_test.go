package proxy

import "testing"

func TestTCPStateSupportWindows(t *testing.T) {
	if err := CheckTCPStateSupport(t.Context()); err != nil {
		t.Fatalf("real SIO_TCP_INFO v0 startup check failed: %v", err)
	}
	t.Log("real SIO_TCP_INFO v0 startup check passed on connected loopback TCP pair")
}
