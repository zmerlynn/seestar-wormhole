package cloudhttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestReceivedRejectionRequiresReauthenticationAfterRetiredExpiry(t *testing.T) {
	for _, late := range []bool{false, true} {
		for _, status := range []int{401, 403} {
			for _, deadline := range []bool{false, true} {
				t.Run(fmt.Sprintf("late=%v/status=%d/deadline=%v", late, status, deadline), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						t.Setenv("SEESTAR_EMAIL", "review@example.invalid")
						t.Setenv("SEESTAR_PASSWORD", "review-password")
						t.Setenv("SEESTAR_TOKEN", "")
						t.Setenv("SEESTAR_SRC_ID", "")
						entered := make(chan struct{})
						remoteEntered, remoteRelease := make(chan struct{}), make(chan struct{})
						logins, profiles := 0, 0
						c, err := NewFromEnvironment(Config{StateDir: t.TempDir(), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
							if strings.HasSuffix(r.URL.Path, "detail") {
								close(remoteEntered)
								<-remoteRelease
								return accountResponse(401, `{"code":401,"data":null}`), nil
							}
							if strings.HasSuffix(r.URL.Path, "login") {
								logins++
								if logins == 2 {
									return &http.Response{StatusCode: status, Body: rejectionBarrierBody{ctx: r.Context(), entered: entered}}, nil
								}
								return accountResponse(200, `{"code":0,"data":{"accessToken":"review-token"}}`), nil
							}
							profiles++
							if profiles == 2 {
								return accountResponse(401, `{"code":401,"data":null}`), nil
							}
							return accountResponse(200, `{"code":0,"data":{"id":"review-user"}}`), nil
						})})
						if err != nil {
							t.Fatal(err)
						}
						defer c.Close()
						if err := c.Login(t.Context()); err != nil {
							t.Fatal(err)
						}
						remoteDone := make(chan error, 1)
						go func() { _, err := c.Detail(t.Context(), Device{"serial", "S50 Pro"}); remoteDone <- err }()
						<-remoteEntered
						ctx, cancel := context.WithCancel(t.Context())
						done := make(chan error, 1)
						go func() { done <- c.CheckProfile(ctx) }()
						<-entered
						// The real remote request authenticated before CheckProfile returns HTTP 401.
						if !late {
							close(remoteRelease)
							if err := <-remoteDone; !errors.Is(err, cloud.ErrReauthentication) {
								t.Fatalf("remote expiry: %v", err)
							}
						}
						if !deadline {
							cancel()
						}
						<-done
						cancel()
						synctest.Wait()
						if late {
							close(remoteRelease)
							if err := <-remoteDone; !errors.Is(err, cloud.ErrReauthentication) {
								t.Fatalf("remote expiry: %v", err)
							}
							synctest.Wait()
						}
						err = c.Login(t.Context())
						var ae *cloud.AccountError
						if !errors.Is(err, cloud.ErrReauthentication) || errors.As(err, &ae) || errors.Is(err, cloud.ErrRejected) || cloud.Disposition(err) != cloud.Denied || logins != 2 {
							t.Fatalf("received credential rejection barrier lost: Login=%v logins=%d; want reauthentication and 2", err, logins)
						}
					})
				})
			}
		}
	}
}
