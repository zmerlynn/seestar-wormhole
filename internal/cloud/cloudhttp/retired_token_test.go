package cloudhttp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestRetiredTokenReplacementUsesCurrentCleanupToken(t *testing.T) {
	for _, body := range []bool{false, true} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("body=%v/deadline=%v", body, deadline), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					t.Setenv("SEESTAR_EMAIL", "review@example.invalid")
					t.Setenv("SEESTAR_PASSWORD", "review-password")
					t.Setenv("SEESTAR_TOKEN", "")
					t.Setenv("SEESTAR_SRC_ID", "")
					var logs lockedLogBuffer
					entered := make(chan struct{})
					remoteEntered, remoteRelease := make(chan struct{}), make(chan struct{})
					logins, profiles, details, stops := 0, 0, 0, 0
					c, err := NewFromEnvironment(Config{StateDir: t.TempDir(), Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
						switch r.URL.Path {
						case "/v1/user/login":
							logins++
							if r.Header.Get("Authorization") != "" || r.Header.Get("X-Astroimg-Authorization") != "" {
								t.Error("credential request inherited authorization")
							}
							switch logins {
							case 1:
								return accountResponse(200, `{"code":0,"data":{"accessToken":"retired-token"}}`), nil
							case 2:
								if body {
									return &http.Response{StatusCode: 200, Body: rejectionBarrierBody{ctx: r.Context(), entered: entered}}, nil
								}
								close(entered)
								<-r.Context().Done()
								return nil, r.Context().Err()
							default:
								return accountResponse(200, `{"code":0,"data":{"accessToken":"replacement-token"}}`), nil
							}
						case "/v1/user/current-profile":
							profiles++
							want := "retired-token"
							if profiles > 2 {
								want = "replacement-token"
							}
							if r.Header.Get("Authorization") != want || r.Header.Get("X-Astroimg-Authorization") != want {
								t.Error("profile used obsolete authorization")
							}
							if profiles == 2 {
								return accountResponse(401, `{"code":401,"data":null}`), nil
							}
							return accountResponse(200, `{"code":0,"data":{"id":"private-profile"}}`), nil
						case "/v1/my-device/detail":
							details++
							if details == 1 {
								if r.Header.Get("Authorization") != "retired-token" {
									t.Error("initial remote request lost snapshot")
								}
								close(remoteEntered)
								<-remoteRelease
								return accountResponse(401, `{"code":401,"msg":"private-profile","data":null}`), nil
							}
							if r.Header.Get("Authorization") != "replacement-token" {
								t.Error("new remote request used retired token")
							}
							return accountResponse(200, `{"code":200,"data":{"deviceSn":"serial","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}}`), nil
						case "/v1/device-p2p/online-link":
							return accountResponse(200, `{"code":200,"data":{"conId":"private-allocation","deviceUid":"private-uid","sdkLicenseKey":"private-license","username":"private-user","password":"private-password","p2pFactory":"TUTK","p2pStatus":1}}`), nil
						case "/v1/device-p2p/stop":
							stops++
							if r.Header.Get("Authorization") != "replacement-token" {
								t.Error("cleanup did not use current authorization")
							}
							return accountResponse(200, `{"code":0,"data":null}`), nil
						default:
							t.Error("unexpected endpoint")
							return nil, cloud.ErrInternal
						}
					})})
					if err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					lease, err := c.Acquire(t.Context(), Device{"serial", "S50 Pro"})
					if err != nil {
						t.Fatal(err)
					}
					defer lease.Close()
					remoteDone := make(chan error, 1)
					go func() { _, err := c.Detail(t.Context(), Device{"serial", "S50 Pro"}); remoteDone <- err }()
					<-remoteEntered
					ctx, cancel := context.WithCancel(t.Context())
					done := make(chan error, 1)
					go func() { done <- c.CheckProfile(ctx) }()
					<-entered
					if !deadline {
						cancel()
					}
					err = ResolveAccount(t.Context(), <-done)
					cancel()
					want := context.Canceled
					if deadline {
						want = context.DeadlineExceeded
					}
					if !errors.Is(err, want) || cloud.Disposition(err) != cloud.Retryable {
						t.Fatalf("interrupted renewal = %v, want retryable interruption", err)
					}
					// No expiry yet: a fresh login may replace the retired token.
					if err := c.Login(t.Context()); err != nil {
						t.Fatal(err)
					}
					close(remoteRelease)
					remoteErr := <-remoteDone
					if !errors.Is(remoteErr, cloud.ErrReauthentication) {
						t.Fatalf("old request = %v, want reauthentication", remoteErr)
					}
					if _, err := c.Detail(t.Context(), Device{"serial", "S50 Pro"}); err != nil {
						t.Fatalf("old expiry erased replacement: %v", err)
					}
					if err := lease.Close(); err != nil {
						t.Fatal(err)
					}
					if logins != 3 || profiles != 3 || details != 2 || stops != 1 {
						t.Fatalf("login/profile/detail/stop = %d/%d/%d/%d, want 3/3/2/1", logins, profiles, details, stops)
					}
					for _, secret := range []string{"review@example.invalid", "review-password", "retired-token", "replacement-token", "private-profile", "private-allocation", "private-uid", "private-license", "private-user", "private-password"} {
						if strings.Contains(logs.String()+fmt.Sprintf("%v %+v %#v", remoteErr, remoteErr, remoteErr), secret) {
							t.Fatal("credential or identity reached logs/errors")
						}
					}
				})
			})
		}
	}
}

func TestRetiredExpiryMailboxDeliversOnce(t *testing.T) {
	o := &accountOwner{expired: make(chan struct{}, 1), done: make(chan struct{})}
	o.publishToken("old")
	o.publishToken("")
	o.expire("unrelated")
	if o.takeExpiry() != "" {
		t.Fatal("unrelated expiry delivered")
	}
	o.expire("old")
	o.publishToken("old") // Identical accepted bytes cannot erase pending expiry.
	if o.takeExpiry() != "old" {
		t.Fatal("retirement lost expiry correlation")
	}
	for range 4096 {
		o.publishToken("old") // Nor can they reset consumed-expiry suppression.
		o.expire("old")
		if o.takeExpiry() != "" {
			t.Fatal("duplicate expiry delivered; terminal outcomes could grow without bound")
		}
	}
	o.publishToken("new")
	o.expire("old")
	if o.takeExpiry() != "" {
		t.Fatal("replacement retained obsolete expiry")
	}
	o.expire("new")
	if o.takeExpiry() != "new" {
		t.Fatal("replacement cannot expire")
	}
	o.cancel = func() { close(o.done) }
	o.close()
	o.expire("new")
	if o.takeExpiry() != "" || len(o.expired) != 0 {
		t.Fatal("shutdown retained expiry")
	}
}
