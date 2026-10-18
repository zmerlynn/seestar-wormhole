package cloudhttp

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func accountSetup(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "Example-Only-123")
	s := httptest.NewTLSServer(h)
	t.Cleanup(s.Close)
	c, err := NewFromEnvironment(Config{BaseURL: s.URL, AccountBaseURL: s.URL, StateDir: t.TempDir(), Transport: s.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
func TestAccountFullLifecycle(t *testing.T) {
	var logins, profiles, stops atomic.Int32
	var expired atomic.Bool
	c := accountSetup(t, func(w http.ResponseWriter, r *http.Request) {
		if !r.Close {
			t.Error("request must close connection")
		}
		switch r.URL.Path {
		case "/v1/user/login":
			n := logins.Add(1)
			b, _ := io.ReadAll(r.Body)
			category := "1"
			if n > 1 {
				category = "0"
			}
			want := `{"authKey":"reader@example.invalid","authType":"email","category":` + category + `,"language":0,"password":"Example-Only-123"}`
			if string(b) != want || r.Method != "POST" || r.Header.Get("Content-Type") != "application/json; charset=UTF-8" {
				t.Error("login wire mismatch")
			}
			if r.Header.Get("Authorization") != "" || r.Header.Get("X-Astroimg-Authorization") != "" {
				t.Error("login inherited token")
			}
			if (r.Header.Get("x-params") == "") != (n > 1) {
				t.Error("renewal header profile")
			}
			token := "first-sentinel"
			if n > 1 {
				token = "second-sentinel"
			}
			io.WriteString(w, `{"code":0,"msg":"ok","data":{"accessToken":"`+token+`","accessExpire":1,"refreshAfter":1}}`)
		case "/v1/user/current-profile":
			profiles.Add(1)
			if r.Header.Get("Authorization") != r.Header.Get("X-Astroimg-Authorization") {
				t.Error("account token headers differ")
			}
			if expired.Load() && r.Header.Get("Authorization") == "first-sentinel" {
				w.WriteHeader(401)
				return
			}
			io.WriteString(w, `{"code":0,"msg":"ok","data":{"id":"profile-sentinel"}}`)
		case "/v1/my-device/detail":
			if r.Header.Get("Authorization") != "second-sentinel" || r.Header.Get("X-Astroimg-Authorization") != "" {
				t.Error("remote token handoff")
			}
			io.WriteString(w, detail)
		case "/v1/device-p2p/online-link":
			if r.URL.Query().Get("srcId") == "" {
				t.Error("missing persistent source")
			}
			io.WriteString(w, online)
		case "/v1/device-p2p/stop":
			stops.Add(1)
			io.WriteString(w, `{"code":0,"msg":"ok","data":null}`)
		default:
			t.Error("unexpected endpoint")
		}
	})
	if err := c.Login(t.Context()); err != nil {
		t.Fatal(err)
	}
	expired.Store(true)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			if err := c.CheckProfile(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if logins.Load() != 2 || profiles.Load() != 18 {
		t.Fatalf("login/profile counts %d/%d", logins.Load(), profiles.Load())
	}
	d := Device{"serial", "S50 Pro"}
	if _, err := c.Detail(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	lease, err := c.Acquire(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	c.Close()
	c.Close()
	if stops.Load() != 1 {
		t.Fatal("missing stop")
	}
	if err := c.Login(t.Context()); !errors.Is(err, cloud.ErrReauthentication) {
		t.Fatal("closed account reused")
	}
}

func TestRemoteExpiryDoesNotRenewAndRecordsUnattemptedStop(t *testing.T) {
	var logins, stops atomic.Int32
	var c *Client
	c = accountSetup(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/user/login":
			logins.Add(1)
			io.WriteString(w, `{"code":0,"msg":"ok","data":{"accessToken":"token-sentinel"}}`)
		case "/v1/user/current-profile":
			io.WriteString(w, `{"code":0,"msg":"ok","data":{"id":"user"}}`)
		case "/v1/device-p2p/online-link":
			// Apply expiry before allowing the response to reach cleanup.
			c.remoteStatus(401, "token-sentinel")
			if err := c.Login(t.Context()); !errors.Is(err, cloud.ErrReauthentication) {
				t.Error("expiry was not applied")
			}
			w.WriteHeader(401)
			io.WriteString(w, online)
		case "/v1/device-p2p/stop":
			stops.Add(1)
			if r.Header.Get("Authorization") != "token-sentinel" {
				t.Error("cleanup lost token")
			}
			io.WriteString(w, `{"code":0,"msg":"ok","data":null}`)
		default:
			t.Error("unexpected request")
		}
	})
	for i := 0; i < 2; i++ {
		_, err := c.Acquire(t.Context(), Device{"serial", "S50 Pro"})
		if !errors.Is(err, cloud.ErrReauthentication) {
			t.Fatalf("expiry: %v", err)
		}
		if strings.Contains(err.Error(), "sentinel") {
			t.Fatal("secret leaked")
		}
	}
	if logins.Load() != 1 || stops.Load() != 0 {
		t.Fatal("remote expiry retried credentials or attempted tokenless cleanup")
	}
}
