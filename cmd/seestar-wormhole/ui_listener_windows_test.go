package main

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
	"golang.org/x/sys/windows"
	"net"
	"os"
	"testing"
)

func TestWrappedWinsockListenerErrors(t *testing.T) {
	for _, tc := range []struct {
		native        error
		device, local uint16
		code          string
	}{
		{windows.WSAEACCES, 80, 80, "http_permission"},
		{windows.WSAEADDRINUSE, 80, 80, "http_conflict"},
		{windows.WSAEADDRINUSE, 4700, 4700, "port_conflict"},
	} {
		err := &net.OpError{Op: "listen", Net: "tcp", Err: &os.SyscallError{Syscall: "bind", Err: tc.native}}
		var e *ui.ActionError
		if !errors.As(uiListenerError(tc.device, tc.local, err), &e) || e.Code != tc.code {
			t.Fatalf("want %s, got %v", tc.code, e)
		}
	}
}
