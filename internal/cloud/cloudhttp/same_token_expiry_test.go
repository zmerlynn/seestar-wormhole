package cloudhttp

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestIdenticalReplayTokenStillExpiresWithoutExtraCredentials(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("late=%v", late), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
				t.Setenv("SEESTAR_PASSWORD", "Review-Only-123")
				remoteEntered := make(chan struct{}, 2)
				remoteRelease := make(chan struct{})
				replayEntered, replayRelease := make(chan struct{}), make(chan struct{})
				logins, profiles := 0, 0
				c, err := NewFromEnvironment(Config{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					switch r.URL.Path {
					case "/v1/user/login":
						logins++
						if logins == 2 {
							close(replayEntered)
							<-replayRelease
						}
						// Independently authored literal reply: replay returns identical bytes.
						return accountResponse(200, `{"data":{"accessToken":"same-raw-token"}}`), nil
					case "/v1/user/current-profile":
						profiles++
						if profiles == 2 {
							return accountResponse(401, `{"data":null}`), nil
						}
						return accountResponse(200, `{"data":null}`), nil
					case "/v1/my-device/detail":
						if r.Header.Get("Authorization") != "same-raw-token" {
							t.Error("remote used wrong authorization")
						}
						remoteEntered <- struct{}{}
						<-remoteRelease
						return accountResponse(401, `{"code":401,"data":null}`), nil
					default:
						t.Error("unexpected request")
						return nil, cloud.ErrInternal
					}
				})})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				if err := c.Login(t.Context()); err != nil {
					t.Fatal(err)
				}
				remoteDone := make(chan error, 2)
				for range 2 {
					go func() { _, err := c.Detail(t.Context(), Device{"serial", "S50 Pro"}); remoteDone <- err }()
				}
				<-remoteEntered
				<-remoteEntered
				profileDone := make(chan error, 1)
				go func() { profileDone <- c.CheckProfile(t.Context()) }()
				<-replayEntered
				if late {
					close(replayRelease)
					if err := <-profileDone; err != nil {
						t.Fatal(err)
					}
					close(remoteRelease)
				} else {
					close(remoteRelease)
					synctest.Wait()
					close(replayRelease)
					if err := <-profileDone; err != nil {
						t.Fatal(err)
					}
				}
				for range 2 {
					if err := <-remoteDone; !errors.Is(err, cloud.ErrReauthentication) {
						t.Fatal("remote expiry lost")
					}
				}
				for range 16 {
					if err := c.Login(t.Context()); !errors.Is(err, cloud.ErrReauthentication) {
						t.Fatal("identical token replacement erased expiry")
					}
				}
				if logins != 2 || profiles != 3 {
					t.Errorf("login/profile=%d/%d, want 2/3 with duplicate expiry", logins, profiles)
				}
			})
		})
	}
}
