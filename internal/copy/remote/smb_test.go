package remote

import (
	"strings"
	"testing"
)

func TestDialRefusesEndpointsWithoutHostAndPort(t *testing.T) {
	// Each is refused before dialing; the reserved .invalid name never resolves.
	for _, endpoint := range []string{"", "fixture.invalid", ":445", "fixture.invalid:", "[::1]", "a:b:c"} {
		s, err := Dial(t.Context(), endpoint, nil)
		if err == nil {
			_ = s.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "is not host:port") {
			t.Errorf("Dial(%q) = %v, want a refusal before dialing", endpoint, err)
		}
	}
}
