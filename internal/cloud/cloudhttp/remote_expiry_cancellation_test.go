package cloudhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestRemoteExpiryCancellation(t *testing.T) {
	for _, acquire := range []bool{false, true} {
		t.Run(map[bool]string{false: "detail", true: "acquire"}[acquire], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
				t.Setenv("SEESTAR_PASSWORD", "Review-Only-123")
				remoteEntered := make(chan struct{}, 2)
				releaseRemote := make(chan struct{})
				profileEntered := make(chan struct{})
				profiles := 0
				var stops atomic.Int32
				c, err := NewFromEnvironment(Config{StateDir: t.TempDir(), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					switch {
					case strings.HasSuffix(r.URL.Path, "login"):
						return accountResponse(200, `{"code":0,"msg":"ok","data":{"accessToken":"opaque"}}`), nil
					case strings.HasSuffix(r.URL.Path, "current-profile"):
						profiles++
						if profiles == 1 {
							return accountResponse(200, `{"code":0,"msg":"ok","data":{"id":"profile"}}`), nil
						}
						close(profileEntered)
						<-r.Context().Done()
						return nil, r.Context().Err()
					case strings.HasSuffix(r.URL.Path, "stop"):
						stops.Add(1)
						if r.Header.Get("Authorization") != "opaque" {
							t.Error("cleanup lost token snapshot")
						}
						return accountResponse(200, `{"code":0,"msg":"ok","data":null}`), nil
					default:
						remoteEntered <- struct{}{}
						<-releaseRemote
						return accountResponse(401, `{"code":1,"msg":"expired","data":{"conId":"allocation"}}`), nil
					}
				})})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				if err = c.Login(t.Context()); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				results := make(chan error, 2)
				for i := 0; i < 2; i++ {
					go func() {
						var err error
						if acquire {
							_, err = c.Acquire(ctx, Device{"serial", "S50 Pro"})
						} else {
							_, err = c.Detail(ctx, Device{"serial", "S50 Pro"})
						}
						results <- err
					}()
				}
				<-remoteEntered
				<-remoteEntered
				profileDone := make(chan error, 1)
				profileCtx, cancelProfile := context.WithCancel(t.Context())
				defer cancelProfile()
				go func() { profileDone <- c.CheckProfile(profileCtx) }()
				<-profileEntered
				close(releaseRemote)
				synctest.Wait()
				cancel()
				synctest.Wait()
				completed := len(results)
				stopped := stops.Load()
				cancelProfile()
				for i := 0; i < 2; i++ {
					if err := <-results; !errors.Is(err, cloud.ErrReauthentication) {
						t.Errorf("got %v, want reauthentication", err)
					}
				}
				<-profileDone
				if err := c.Login(t.Context()); !errors.Is(err, cloud.ErrReauthentication) {
					t.Errorf("expiry lost after cancellation: %v", err)
				}
				if acquire && stopped != 2 {
					t.Errorf("cleanup stops=%d, want 2 before profile finishes", stopped)
				}
				if completed != 2 {
					t.Fatalf("canceled remote calls completed=%d, want 2; expiry mailbox blocked behind unrelated profile I/O", completed)
				}
			})
		})
	}
}

func TestRemoteExpiryCannotEraseRenewedToken(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(map[bool]string{false: "during-renewal", true: "after-renewal"}[late], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
				t.Setenv("SEESTAR_PASSWORD", "Review-Only-123")
				remoteEntered, releaseRemote := make(chan struct{}), make(chan struct{})
				renewalEntered, releaseRenewal := make(chan struct{}), make(chan struct{})
				logins, profiles := 0, 0
				c, err := NewFromEnvironment(Config{StateDir: t.TempDir(), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					switch {
					case strings.HasSuffix(r.URL.Path, "login"):
						logins++
						if logins == 1 {
							return accountResponse(200, `{"code":0,"msg":"ok","data":{"accessToken":"old"}}`), nil
						}
						close(renewalEntered)
						<-releaseRenewal
						return accountResponse(200, `{"code":0,"msg":"ok","data":{"accessToken":"new"}}`), nil
					case strings.HasSuffix(r.URL.Path, "current-profile"):
						profiles++
						if profiles == 2 {
							return accountResponse(401, `{"code":1,"msg":"expired","data":null}`), nil
						}
						return accountResponse(200, `{"code":0,"msg":"ok","data":{"id":"profile"}}`), nil
					default:
						if r.Header.Get("Authorization") != "old" {
							t.Error("remote did not snapshot old token")
						}
						close(remoteEntered)
						<-releaseRemote
						return accountResponse(401, `{"code":1,"msg":"expired","data":null}`), nil
					}
				})})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				if err = c.Login(t.Context()); err != nil {
					t.Fatal(err)
				}
				remoteDone := make(chan error, 1)
				go func() { _, err := c.Detail(t.Context(), Device{"serial", "S50 Pro"}); remoteDone <- err }()
				<-remoteEntered
				profileDone := make(chan error, 1)
				go func() { profileDone <- c.CheckProfile(t.Context()) }()
				<-renewalEntered
				if late {
					close(releaseRenewal)
					if err := <-profileDone; err != nil {
						t.Fatal(err)
					}
					close(releaseRemote)
				} else {
					close(releaseRemote)
					synctest.Wait()
					close(releaseRenewal)
					if err := <-profileDone; err != nil {
						t.Fatal(err)
					}
				}
				if err := <-remoteDone; !errors.Is(err, cloud.ErrReauthentication) {
					t.Fatalf("remote err=%v, want reauthentication", err)
				}
				request, err := c.authorized(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if request.token != "new" || logins != 2 || profiles != 3 {
					t.Fatalf("token/calls incorrect: logins=%d profiles=%d", logins, profiles)
				}
			})
		})
	}
}
