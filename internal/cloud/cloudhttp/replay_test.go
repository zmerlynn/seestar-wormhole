package cloudhttp

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

// An injected transport may already hold a warm connection from another user,
// even though every cloud request closes its connection. A lost allocation reply
// must never be replayed.
func TestNoReplayOnLostAllocationReply(t *testing.T) {
	var allocs atomic.Int32
	c := setup(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "detail") {
			io.WriteString(w, detail)
			return
		}
		allocs.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	})
	req, err := http.NewRequestWithContext(t.Context(), "GET", c.base.String()+"/v1/my-device/detail", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("warm connection: read=%v close=%v", readErr, closeErr)
	}
	var reused atomic.Bool
	ctx := httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		if info.Reused {
			reused.Store(true)
		}
	}})
	lease, err := c.Acquire(ctx, Device{Serial: "serial", Model: "S50 Pro"})
	if lease != nil {
		defer lease.Close()
		t.Fatal("lost allocation reply handed off a lease")
	}
	if !reused.Load() {
		t.Fatal("probe did not exercise a reused connection")
	}
	if !errors.Is(err, cloud.ErrTransport) || allocs.Load() != 1 {
		t.Fatalf("lost allocation response: err=%v requests=%d, want transport error and 1 request", err, allocs.Load())
	}
}
