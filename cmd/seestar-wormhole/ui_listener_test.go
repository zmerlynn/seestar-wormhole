package main

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
	"os"
	"syscall"
	"testing"
)

func TestUIListenerErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		device, local uint16
		err           error
		code          string
	}{
		{80, 80, os.ErrPermission, "http_permission"}, {80, 80, syscall.EADDRINUSE, "http_conflict"},
		{4700, 4700, syscall.EADDRINUSE, "port_conflict"}, {4700, 4700, errors.New("private"), "bind"},
	} {
		var e *ui.ActionError
		if !errors.As(uiListenerError(tc.device, tc.local, tc.err), &e) || e.Code != tc.code {
			t.Fatalf("want %s: %v", tc.code, e)
		}
	}
}
