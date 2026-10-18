package cloudhttp

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestOptionalAccountCodeProfileCompletionLifecycle(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "Example-Only-123")
	logins, profiles, stops := 0, 0, 0
	c, err := NewFromEnvironment(Config{StateDir: t.TempDir(), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/user/login":
			logins++
			if logins == 1 {
				return accountResponse(200, `{"data":{"accessToken":"first"}}`), nil
			}
			return accountResponse(201, `{"code":null,"data":{"accessToken":"renewed"}}`), nil
		case "/v1/user/current-profile":
			profiles++
			want := "first"
			if profiles == 3 {
				want = "renewed"
			}
			if r.Header.Get("Authorization") != want || r.Header.Get("X-Astroimg-Authorization") != want {
				t.Error("profile did not use installed token")
			}
			switch profiles {
			case 1:
				return accountResponse(201, `{}`), nil
			case 2:
				return accountResponse(401, `{"code":null}`), nil
			default:
				return accountResponse(204, ``), nil
			}
		case "/v1/my-device/detail":
			if r.Header.Get("Authorization") != "renewed" {
				t.Error("detail did not use renewed token")
			}
			return accountResponse(200, detail), nil
		case "/v1/device-p2p/online-link":
			return accountResponse(200, online), nil
		case "/v1/device-p2p/stop":
			stops++
			if r.Header.Get("Authorization") != "renewed" {
				t.Error("release lost allocation authorization")
			}
			return accountResponse(200, `{"code":0}`), nil
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			return accountResponse(500, ``), nil
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckProfile(t.Context()); err != nil {
		t.Fatal(err)
	}
	d := Device{"serial", "S50 Pro"}
	if _, err := c.Detail(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	lease, err := c.Acquire(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	}
	c.Close()
	if logins != 2 || profiles != 3 || stops != 1 {
		t.Fatalf("login/profile/stop calls=%d/%d/%d, want 2/3/1", logins, profiles, stops)
	}
}

func TestProfileCompletionRequiresCompleteIO(t *testing.T) {
	for _, tt := range []struct {
		status int
		body   string
	}{
		{201, `{}`},
		{204, ``},
	} {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
			t.Setenv("SEESTAR_PASSWORD", "Example-Only-123")
			profiles := 0
			c, err := NewFromEnvironment(Config{StateDir: t.TempDir(), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "login") {
					return accountResponse(200, `{"data":{"accessToken":"opaque"}}`), nil
				}
				profiles++
				if profiles == 1 {
					return accountResponse(204, ``), nil
				}
				return &http.Response{StatusCode: tt.status, Body: &terminalErrorBody{Reader: strings.NewReader(tt.body), err: io.ErrUnexpectedEOF}}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if err := c.Login(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := c.CheckProfile(t.Context()); !errors.Is(err, cloud.ErrTransport) {
				t.Fatalf("got %v, want interrupted profile failure", err)
			}
		})
	}
}
