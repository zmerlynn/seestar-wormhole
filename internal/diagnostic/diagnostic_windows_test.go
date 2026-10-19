//go:build windows

package diagnostic

import (
	"fmt"
	"golang.org/x/sys/windows"
	"testing"
)

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
