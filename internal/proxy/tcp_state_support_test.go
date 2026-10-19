package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"syscall"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func TestTCPStateSupportQuery(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		err          error
	}{
		{"success", "none", nil},
		{"failed", "operation_failed", syscall.EINVAL},
		{"short", "operation_failed", io.ErrUnexpectedEOF},
		{"permission", "permission_denied", syscall.EACCES},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var inspected net.Conn
			err := checkTCPStateSupport(t.Context(), func(conn net.Conn) error {
				inspected = conn
				for _, addr := range []net.Addr{conn.LocalAddr(), conn.RemoteAddr()} {
					if !addr.(*net.TCPAddr).IP.IsLoopback() {
						t.Fatalf("not a loopback TCP pair: %v", addr)
					}
				}
				return tc.err
			})
			phase, reason := diagnostic.Fields(err, "tcp_state_query")
			if !errors.Is(err, tc.err) || phase != "tcp_state_query" || reason != tc.reason || inspected == nil {
				t.Fatalf("query: err=%v phase=%s reason=%s inspected=%v", err, phase, reason, inspected)
			}
			if _, err := inspected.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("inspected socket not closed: %v", err)
			}
		})
	}
}

func TestTCPStateSupportLoopbackFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := checkTCPStateSupport(ctx, func(net.Conn) error {
		t.Fatal("queried without a connected pair")
		return nil
	})
	phase, reason := diagnostic.Fields(err, "")
	if phase != "tcp_state_loopback" || reason != "loopback_unavailable" {
		t.Fatalf("loopback failure: phase=%s reason=%s err=%v", phase, reason, err)
	}
	err = CheckTCPStateSupport(ctx)
	if runtime.GOOS != "windows" {
		if err != nil {
			t.Fatalf("non-Windows check must be a no-op: %v", err)
		}
	} else {
		phase, reason := diagnostic.Fields(err, "")
		if phase != "tcp_state_loopback" || reason != "loopback_unavailable" {
			t.Fatalf("Windows public check must refuse canceled loopback setup: phase=%s reason=%s err=%v", phase, reason, err)
		}
		t.Logf("Windows public check refused canceled loopback setup: phase=%s reason=%s", phase, reason)
	}
}
