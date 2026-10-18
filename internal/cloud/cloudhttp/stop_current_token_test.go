package cloudhttp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

// Literal replies and stop body are independent of the production codecs.
func TestStopReadsCurrentTokenWithoutAccountIO(t *testing.T) {
	for _, state := range []string{"before-login", "installed-during-profile", "expired-during-profile", "retired-after-cancellation"} {
		t.Run(state, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
				t.Setenv("SEESTAR_PASSWORD", "Review-Only-123")
				var logs lockedLogBuffer
				entered := make(chan struct{})
				logins, profiles, stops := 0, 0, 0
				c, err := NewFromEnvironment(Config{Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					switch r.URL.Path {
					case "/v1/user/login":
						logins++
						if logins == 2 && state == "retired-after-cancellation" {
							close(entered)
							<-r.Context().Done()
							return nil, r.Context().Err()
						}
						if logins == 1 {
							return accountResponse(200, `{"data":{"accessToken":"first-secret"}}`), nil
						}
						return accountResponse(200, `{"data":{"accessToken":"current-secret"}}`), nil
					case "/v1/user/current-profile":
						profiles++
						if profiles == 2 {
							return accountResponse(401, `{"code":401,"data":null}`), nil
						}
						if profiles == 3 {
							close(entered)
							<-r.Context().Done()
							return nil, r.Context().Err()
						}
						return accountResponse(200, `{"data":null}`), nil
					case "/v1/device-p2p/stop":
						stops++
						if r.Header.Get("Authorization") != "current-secret" {
							t.Error("stop did not use currently installed token")
						}
						b, _ := io.ReadAll(r.Body)
						if r.Method != "POST" || string(b) != `{"conId":"allocation-secret"}` {
							t.Error("stop must carry only literal conId")
						}
						if state == "expired-during-profile" {
							return accountResponse(401, `{"code":401,"data":null}`), nil
						}
						return accountResponse(200, `{"code":200,"data":null}`), nil
					default:
						t.Error("unexpected credential or remote request")
						return nil, cloud.ErrInternal
					}
				})})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				a, _, err := cloud.ParseOnline([]byte(`{"code":200,"data":{"conId":"allocation-secret","deviceUid":"uid-secret","sdkLicenseKey":"license-secret","username":"user-secret","password":"password-secret"}}`))
				if err != nil {
					t.Fatal(err)
				}
				lease := &Lease{client: c, allocation: a, done: make(chan struct{})}
				var done chan error
				cancel := func() {}
				if state != "before-login" {
					if err := c.Login(t.Context()); err != nil {
						t.Fatal(err)
					}
					ctx, stop := context.WithCancel(t.Context())
					cancel = stop
					defer cancel()
					done = make(chan error, 1)
					go func() { done <- c.CheckProfile(ctx) }()
					<-entered
					if state == "retired-after-cancellation" {
						cancel()
						if err := ResolveAccount(t.Context(), <-done); !errors.Is(err, context.Canceled) {
							t.Fatal(err)
						}
					}
				}
				err = lease.Close()
				if state == "installed-during-profile" || state == "expired-during-profile" {
					wantFailure := state == "expired-during-profile"
					if (err != nil) != wantFailure || stops != 1 || logins != 2 {
						t.Errorf("current stop: err=%v stops=%d logins=%d", err, stops, logins)
					}
					cancel()
					_ = ResolveAccount(t.Context(), <-done)
					if wantFailure {
						if err := c.Login(t.Context()); !errors.Is(err, cloud.ErrReauthentication) || logins != 2 {
							t.Error("stop 401 for installed token was lost during profile I/O")
						}
					}
				} else {
					if !errors.Is(err, cloud.ErrReauthentication) || stops != 0 || logins != map[string]int{"before-login": 0, "retired-after-cancellation": 2}[state] {
						t.Errorf("tokenless stop: err=%v stops=%d logins=%d; want not attempted, no credential send", err, stops, logins)
					}
					if !strings.Contains(logs.String(), `"attempted":false`) {
						t.Error("tokenless stop not recorded as unattempted")
					}
				}
				_ = lease.Close()
				for _, secret := range []string{"reader@example.invalid", "Review-Only-123", "first-secret", "current-secret", "allocation-secret", "uid-secret", "license-secret", "user-secret", "password-secret"} {
					if strings.Contains(logs.String(), secret) {
						t.Error("secret reached diagnostics")
					}
				}
			})
		})
	}
}

func TestFailedAllocationCleanupUsesReplacementToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
		t.Setenv("SEESTAR_PASSWORD", "Review-Only-123")
		entered, release := make(chan struct{}), make(chan struct{})
		logins, profiles, stops := 0, 0, 0
		c, err := NewFromEnvironment(Config{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/v1/user/login":
				logins++
				if logins == 1 {
					return accountResponse(200, `{"data":{"accessToken":"first-secret"}}`), nil
				}
				return accountResponse(200, `{"data":{"accessToken":"current-secret"}}`), nil
			case "/v1/user/current-profile":
				profiles++
				if profiles == 2 {
					return accountResponse(401, `{"data":null}`), nil
				}
				return accountResponse(200, `{"data":null}`), nil
			case "/v1/device-p2p/online-link":
				close(entered)
				<-release
				return accountResponse(400, `{"code":300405,"data":{"conId":"allocation-secret"}}`), nil
			case "/v1/device-p2p/stop":
				stops++
				if r.Header.Get("Authorization") != "current-secret" {
					t.Error("failed allocation cleanup used obsolete token")
				}
				b, _ := io.ReadAll(r.Body)
				if string(b) != `{"conId":"allocation-secret"}` {
					t.Error("stop body changed")
				}
				return accountResponse(200, `{"code":200,"data":null}`), nil
			default:
				return nil, cloud.ErrInternal
			}
		})})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		done := make(chan error, 1)
		go func() { _, err := c.Acquire(t.Context(), Device{"serial", "S50 Pro"}); done <- err }()
		<-entered
		if err := c.CheckProfile(t.Context()); err != nil {
			t.Fatal(err)
		}
		close(release)
		if err := <-done; !errors.Is(err, cloud.ErrRejected) || errors.Is(err, ErrCleanup) {
			t.Fatalf("allocation outcome=%v", err)
		}
		if stops != 1 || logins != 2 {
			t.Fatalf("stops=%d logins=%d; want 1/2", stops, logins)
		}
	})
}
