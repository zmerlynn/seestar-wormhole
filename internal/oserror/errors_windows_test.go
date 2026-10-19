//go:build windows

package oserror

import (
	"fmt"
	"golang.org/x/sys/windows"
	"syscall"
	"testing"
)

func TestNativeSocketErrors(t *testing.T) {
	for _, tc := range []struct{ native, portable error }{
		{windows.WSAECONNREFUSED, syscall.ECONNREFUSED},
		{windows.WSAECONNRESET, syscall.ECONNRESET},
		{windows.WSAEADDRINUSE, syscall.EADDRINUSE},
		{windows.WSAEADDRNOTAVAIL, syscall.EADDRNOTAVAIL},
		{windows.ERROR_BROKEN_PIPE, syscall.EPIPE},
	} {
		if !Is(fmt.Errorf("private sentinel: %w", tc.native), tc.portable) {
			t.Fatalf("native %v not recognized as %v", tc.native, tc.portable)
		}
		if Is(tc.native, syscall.ELOOP) {
			t.Fatal("unrelated native error misclassified")
		}
	}
}
