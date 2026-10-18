package main

import (
	"context"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"net"
	"os"
	"runtime"
	"testing"
)

// Sixteen fixture files force all requested workers into real SMB file reads
// before SIGINT. The barrier is inside Read, after the backend has read and copied a data chunk.
func TestPullSIGINTConcurrentCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows console signal gate required")
	}
	ep := os.Getenv("SEESTAR_TEST_MANY_SAMBA_ADDR")
	if ep == "" {
		t.Skip("16-file container Samba fixture required")
	}
	for _, transfers := range []int{4, 8} {
		for _, via := range []bool{false, true} {
			t.Run(fmt.Sprintf("transfers=%d/via=%t", transfers, via), func(t *testing.T) {
				testPullSIGINTPhase(t, "copy", via, false, "SEESTAR_TEST_SAMBA_ADDR="+ep, fmt.Sprintf("SEESTAR_TEST_SIGNAL_TRANSFERS=%d", transfers))
			})
		}
	}
}

// Use the production forwarding path; the ambient proxy lifetime is independent
// of the child pull's signal context and owns no synthetic session release.
func signalSMBProxy(t *testing.T, ep string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- proxy.Serve(ctx, &pullSessionFixture{addr: ep}, []proxy.Forward{{Listener: listener, Port: 445}}, 16, nil)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return listener.Addr().String()
}
