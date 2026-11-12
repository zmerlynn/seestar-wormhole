package remote

import (
	"net"
	"strings"
	"testing"
)

// TestDialErrorsNameTheEndpoint checks that a failed dial names the
// endpoint it dialed, so a log of it says which telescope didn't answer.
func TestDialErrorsNameTheEndpoint(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := l.Addr().String()
	l.Close()
	if _, err := Dial(t.Context(), endpoint, nil); err == nil || !strings.Contains(err.Error(), endpoint) {
		t.Fatalf("Dial error %v, want one naming %s", err, endpoint)
	}
	if err := Reach(t.Context(), endpoint); err == nil || !strings.Contains(err.Error(), endpoint) {
		t.Fatalf("Reach error %v, want one naming %s", err, endpoint)
	}
}
