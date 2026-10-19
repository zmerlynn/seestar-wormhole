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

func TestRemoteExpiryDuringCanceledCredentialReplay(t *testing.T) {
	for _, body := range []bool{false, true} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("body=%v/deadline=%v", body, deadline), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					t.Setenv("SEESTAR_EMAIL", "review@example.invalid")
					t.Setenv("SEESTAR_PASSWORD", "review-password")

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
								if body {
									return &http.Response{StatusCode: 200, Body: rejectionBarrierBody{ctx: r.Context(), entered: entered}}, nil
								}
								close(entered)
								<-r.Context().Done()
								return nil, r.Context().Err()
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
					close(remoteRelease)
					if err := <-remoteDone; !errors.Is(err, cloud.ErrReauthentication) {
						t.Fatalf("remote expiry: %v", err)
					}
					if !deadline {
						cancel()
					}
					<-done
					cancel()
					synctest.Wait()
					err = c.Login(t.Context())
					if !errors.Is(err, cloud.ErrReauthentication) || logins != 2 {
						t.Fatalf("remote expiry lost on canceled replay: Login=%v logins=%d; want reauthentication and 2", err, logins)
					}
				})
			})
		}
	}
}
