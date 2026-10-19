package cloudhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

const accountSentinel = "secret-sentinel"

func TestAccountEveryFailureIsSecretSafe(t *testing.T) {
	for _, stage := range []string{"login", "profile", "renewal"} {
		for _, mode := range []string{"400", "401", "403", "404", "423", "429", "500", "redirect", "empty", "nil-body", "truncated", "oversized", "wrong-type", "read-error", "transport-error", "panic", "cancel"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				var sink lockedLogBuffer
				old := log.Writer()
				log.SetOutput(&sink)
				defer log.SetOutput(old)
				logger := slog.New(slog.NewTextHandler(&sink, nil))
				t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
				t.Setenv("SEESTAR_PASSWORD", accountSentinel)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				logins, requests := 0, 0
				c, err := NewFromEnvironment(Config{StateDir: t.TempDir(), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					requests++
					login := strings.HasSuffix(r.URL.Path, "login")
					if login {
						logins++
					}
					failure := stage == "login" || stage == "profile" && !login || stage == "renewal" && login && logins > 1
					if !failure {
						if login {
							return accountResponse(200, `{"code":0,"msg":"ok","data":{"accessToken":"`+accountSentinel+`"}}`), nil
						}
						return accountResponse(401, `{"code":9,"msg":"`+accountSentinel+`","data":null}`), nil
					}
					b := `{"code":7788,"msg":"` + accountSentinel + `","data":null}`
					switch mode {
					case "400", "401", "403", "404", "423", "429", "500":
						var code int
						fmt.Sscan(mode, &code)
						return accountResponse(code, b), nil
					case "redirect":
						resp := accountResponse(307, b)
						resp.Header = http.Header{"Location": []string{"https://example.invalid/" + accountSentinel}}
						return resp, nil
					case "empty":
						return accountResponse(200, ""), nil
					case "nil-body":
						return &http.Response{StatusCode: 200}, nil
					case "truncated":
						return accountResponse(200, `{"code":0,"msg":"`+accountSentinel), nil
					case "oversized":
						return accountResponse(200, b+strings.Repeat(" ", cloud.MaxBody)), nil
					case "wrong-type":
						if stage == "profile" {
							return accountResponse(200, `{"code":false,"msg":"`+accountSentinel+`","data":{"id":12}}`), nil
						}
						return accountResponse(200, `{"code":0,"msg":"ok","data":{"accessToken":12,"id":12}}`), nil
					case "read-error":
						return &http.Response{StatusCode: 200, Body: &terminalErrorBody{Reader: strings.NewReader(b), err: errors.New(accountSentinel)}}, nil
					case "transport-error":
						return nil, errors.New(accountSentinel)
					case "panic":
						panic(accountSentinel)
					case "cancel":
						cancel()
						return nil, errors.New(accountSentinel)
					}
					panic("test mode")
				})})
				if err != nil {
					t.Fatal(err)
				}
				err = c.Login(ctx)
				if err == nil {
					t.Fatal("failure accepted")
				}
				logger.Error("failure", "error", err, "client", c)
				formatted := fmt.Sprintf("%v %+v %#v", err, err, err)
				c.Close()
				if strings.Contains(sink.String()+formatted, accountSentinel) {
					t.Fatal("secret reached logger or error formatting")
				}
				if logins > 3 || requests > 6 {
					t.Fatal("unbounded retries")
				}
			})
		}
	}
}
func accountResponse(status int, b string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(b))}
}

func TestAccountCloseCancelsAndJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
		t.Setenv("SEESTAR_PASSWORD", accountSentinel)
		started := make(chan struct{})
		c, err := NewFromEnvironment(Config{StateDir: t.TempDir(), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			close(started)
			<-r.Context().Done()
			return nil, errors.New(accountSentinel)
		})})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- c.Login(t.Context()) }()
		<-started
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Go(func() { _ = c.CheckProfile(t.Context()) })
		}
		c.Close()
		wg.Wait()
		if err := <-done; err == nil {
			t.Fatal("close did not cancel login")
		}
		select {
		case <-c.account.done:
		default:
			t.Fatal("owner leaked")
		}
		synctest.Wait()
	})
}

func TestAccountAdmissionAndRenewalDeadlines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
		t.Setenv("SEESTAR_PASSWORD", accountSentinel)
		calls := 0
		started := make(chan struct{})
		c, err := NewFromEnvironment(Config{StateDir: t.TempDir(), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return accountResponse(200, `{"code":0,"msg":"ok","data":{"accessToken":"opaque"}}`), nil
			}
			if calls == 2 {
				return accountResponse(401, ""), nil
			}
			close(started)
			<-r.Context().Done()
			return nil, r.Context().Err()
		})})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		begin := time.Now()
		done := make(chan error, 1)
		go func() { done <- c.Login(t.Context()) }()
		<-started
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := c.CheckProfile(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal("admission ignored cancellation")
		}
		if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("renewal deadline missing")
		}
		if time.Since(begin) != 10*time.Second || calls != 3 {
			t.Fatal("renewal timeout/count")
		}
	})
}

func TestAccountExpiryQueuedDuringConcurrentProfile(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", accountSentinel)
	entered, release := make(chan struct{}), make(chan struct{})
	profiles := 0
	c, err := NewFromEnvironment(Config{StateDir: t.TempDir(), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "login") {
			return accountResponse(200, `{"code":0,"msg":"ok","data":{"accessToken":"opaque"}}`), nil
		}
		profiles++
		if profiles == 2 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return accountResponse(200, `{"code":0,"msg":"ok","data":{"id":"profile"}}`), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login(t.Context()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.CheckProfile(t.Context()) }()
	<-entered
	c.account.expire("opaque")
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := c.Login(t.Context()); !errors.Is(err, cloud.ErrReauthentication) {
		t.Fatal("remote expiry was lost behind a concurrent account operation")
	}
}

func TestClosedAccountDiscardsPendingTokenNotification(t *testing.T) {
	// A notification may remain queued when shutdown wins the owner's select.
	o := &accountOwner{expired: make(chan struct{}, 1), done: make(chan struct{}), correlationToken: accountSentinel, pendingExpiry: true}
	o.expired <- struct{}{}
	o.cancel = func() {
		select {
		case <-o.done:
		default:
			close(o.done)
		}
	}
	o.close()
	if len(o.expired) != 0 || o.correlationToken != "" || o.pendingExpiry || o.expiryTaken {
		t.Fatal("shutdown retained token notification")
	}
	o.expire(accountSentinel)
	if len(o.expired) != 0 {
		t.Fatal("closed owner accepted token notification")
	}
}
