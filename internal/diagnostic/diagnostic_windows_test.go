//go:build windows

package diagnostic

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"testing"
)

func TestWindowsWinsockPermissionDiagnostic(t *testing.T) {
	err := Wrap("tcp_state_query", "", fmt.Errorf("private sentinel: %w", windows.WSAEACCES))
	phase, reason := Fields(err, "")
	if phase != "tcp_state_query" || reason != "permission_denied" || !errors.Is(err, windows.WSAEACCES) {
		t.Fatalf("native WSAEACCES (10013): phase=%s reason=%s err=%v", phase, reason, err)
	}
	t.Logf("native WSAEACCES (10013): phase=%s reason=%s", phase, reason)
}

func TestWindowsNativeDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{windows.WSAECONNREFUSED, "connection_refused"},
		{windows.WSAECONNRESET, "connection_reset"},
		{windows.WSAEADDRINUSE, "address_in_use"},
		{windows.WSAEADDRNOTAVAIL, "address_unavailable"},
		{windows.ERROR_BROKEN_PIPE, "broken_pipe"},
		{windows.ERROR_DIRECTORY, "not_a_directory"},
		{windows.ERROR_DISK_FULL, "no_space"},
	} {
		if got := Class(fmt.Errorf("private sentinel: %w", tc.err)); got != tc.want {
			t.Errorf("native %v: got %s, want %s", tc.err, got, tc.want)
		}
	}
}
