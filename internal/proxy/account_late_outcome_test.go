package proxy

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
)

// A blocked daemon alone cannot prove the owner's barrier: make another direct
// authorization call after the late result has actually been published.
func TestLateAccountCompletionBlocksLaterAuthorization(t *testing.T) {
	for _, phase := range []string{"login", "profile-malformed", "profile-denied"} {
		t.Run(phase, func(t *testing.T) {
			t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
			t.Setenv("SEESTAR_PASSWORD", "fixture-password")
			synctest.Test(t, func(t *testing.T) {
				var transport cloudhttp.Transport
				var state *r4Cloud
				want := cloud.Malformed
				switch phase {
				case "login":
					tr := &r4AccountDeadline{r4Cloud: r4Cloud{stops: map[string]int{}}, body: `{"code":0,"data":{"accessToken":123}}`}
					transport, state = tr, &tr.r4Cloud
				case "profile-malformed":
					tr := &r4ProfileDeadline{r4Cloud: r4Cloud{stops: map[string]int{}}, status: 200, body: `{"code":"invalid","data":null}`}
					transport, state = tr, &tr.r4Cloud
				case "profile-denied":
					tr := &r4ProfileDeadline{r4Cloud: r4Cloud{stops: map[string]int{}}, status: 409, body: `{"code":0,"data":null}`}
					transport, state = tr, &tr.r4Cloud
					want = cloud.Denied
				}
				c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: transport, StateDir: t.TempDir(), Timeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				first := c.Login(t.Context())
				if !errors.Is(first, context.DeadlineExceeded) || cloud.Disposition(first) < cloud.Malformed {
					t.Fatalf("caller=%v rank=%d want deadline and uncertainty", first, cloud.Disposition(first))
				}
				<-time.After(40 * time.Millisecond)
				synctest.Wait()
				_, err = c.Acquire(t.Context(), cloudhttp.Device{Serial: "sim", Model: "S50 Pro"})
				if cloud.Disposition(err) != want || state.allocations != 0 {
					t.Fatalf("late authorization=%v rank=%d allocations=%d want %d/no allocation", err, cloud.Disposition(err), state.allocations, want)
				}
			})
		})
	}
}
