package cloudhttp

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func TestRejectedReplayLaterOperationsRequireReauthentication(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
				t.Setenv("SEESTAR_PASSWORD", "Review-Only-123")
				var logs lockedLogBuffer
				logins, profiles, remote := 0, 0, 0
				c, err := NewFromEnvironment(Config{Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					switch r.URL.Path {
					case "/v1/user/login":
						logins++
						if logins == 1 {
							return accountResponse(200, `{"data":{"accessToken":"token-secret"}}`), nil
						}
						return accountResponse(status, `{"code":9876,"msg":"identity-secret","data":null}`), nil
					case "/v1/user/current-profile":
						profiles++
						if profiles == 1 {
							return accountResponse(200, `{"data":null}`), nil
						}
						return accountResponse(401, `{"code":401,"data":null}`), nil
					default:
						remote++
						return accountResponse(200, `{"code":200,"data":null}`), nil
					}
				})})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				if err := c.Login(t.Context()); err != nil {
					t.Fatal(err)
				}
				immediate := c.CheckProfile(t.Context())
				var ae *cloud.AccountError
				if !errors.As(immediate, &ae) || ae.Status != status || !errors.Is(immediate, cloud.ErrRejected) {
					t.Fatal("original replay must preserve observed status")
				}
				for _, op := range []string{"login", "profile", "detail", "acquire"} {
					var err error
					switch op {
					case "login":
						err = c.Login(t.Context())
					case "profile":
						err = c.CheckProfile(t.Context())
					case "detail":
						_, err = c.Detail(t.Context(), Device{"serial", "S50 Pro"})
					case "acquire":
						_, err = c.Acquire(t.Context(), Device{"serial", "S50 Pro"})
					}
					ae = nil
					if !errors.Is(err, cloud.ErrReauthentication) || errors.As(err, &ae) || errors.Is(err, cloud.ErrRejected) || cloud.Disposition(err) != cloud.Denied || diagnostic.Class(err) != "reauthentication_required" {
						t.Errorf("%s inherited replay cause: class=%s status-retained=%t; want reauthentication without cause mapping", op, diagnostic.Class(err), ae != nil)
					}
					diagnostic.LogFinal(c.logger, "later_failed", op, err, nil)
					for _, secret := range []string{"reader@example.invalid", "Review-Only-123", "token-secret", "identity-secret", "9876"} {
						if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err)+logs.String(), secret) {
							t.Error("secret reached logs or errors")
						}
					}
				}
				if logins != 2 || profiles != 2 || remote != 0 {
					t.Errorf("requests login/profile/remote=%d/%d/%d; want 2/2/0", logins, profiles, remote)
				}
			})
		})
	}
}
