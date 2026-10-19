package cloudhttp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestCloudMetamorphicAccountHTTP(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "Example-Only-123")
	for _, controls := range [][]byte{{0, 0}, {1, 2}, {2, 0}, {4, 3}, {8, 0}, {31, 3}} {
		t.Run(fmt.Sprintf("%x", controls), func(t *testing.T) {
			login := mutateCloudFixture(t, 0, controls)
			profile := mutateCloudFixture(t, 1, controls)
			var logs bytes.Buffer
			profiles := 0
			c, err := NewFromEnvironment(Config{
				BaseURL: "https://cloud.example.invalid", AccountBaseURL: "https://account.example.invalid",
				Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
				Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					b := login
					if r.URL.Path == "/v1/user/current-profile" {
						profiles++
						if r.Header.Get("Authorization") != "synthetic-token-sentinel" {
							t.Error("tolerable mutation changed installed token")
						}
						b = profile
						// Demonstrate that profile contents do not replace the
						// independently installed login token.
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b))}, nil
				}),
			})
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
			snapshot, err := c.authorized(t.Context())
			if err != nil || snapshot.token != "synthetic-token-sentinel" || profiles < 2 {
				t.Fatal("account mutation changed token/profile completion")
			}
			assertCloudSecretSafe(t, err, logs.String())
		})
	}
}

func TestCloudMetamorphicHTTPRefusalSecrecy(t *testing.T) {
	for _, family := range []int{2, 3, 4, 5, 6} {
		f := metamorphicFixtures[family]
		for _, status := range []int{400, 401, 403} {
			t.Run(fmt.Sprintf("%s/%d", f.name, status), func(t *testing.T) {
				root := metamorphicObject(t, mutateCloudFixture(t, family, []byte{31, 3}))
				root["code"] = []byte(`300404`)
				root["msg"] = []byte(`"synthetic-password-sentinel"`)
				body := metamorphicJSON(t, root)
				var logs bytes.Buffer
				c, err := newAuthenticatedFixture(t, Config{
					Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
					Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
						// Allocation failures must still clean up a returned conId.
						if r.URL.Path == "/v1/device-p2p/stop" && f.name != "stop" {
							return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":200}`))}, nil
						}
						return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {`application/json; secret="synthetic-token-sentinel"`}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
					}),
				})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				d := Device{Serial: "synthetic-serial-sentinel", Model: "S50 Pro"}
				switch f.name {
				case "devices":
					_, err = c.SelectDevice(t.Context(), d)
				case "detail":
					_, err = c.Detail(t.Context(), d)
				case "lan_detail":
					_, err = c.DetailForLAN(t.Context(), d)
				case "allocation":
					_, err = c.Acquire(t.Context(), d)
				case "stop":
					_, ids, _ := cloud.ParseOnline([]byte(metamorphicFixtures[5].body))
					err = c.stopWithContext(t.Context(), ids[0])
				}
				if err == nil {
					t.Fatal("HTTP refusal accepted")
				}
				if status == 401 && !errors.Is(err, cloud.ErrUnauthorized) {
					t.Fatal("auth refusal lost")
				}
				assertCloudSecretSafe(t, err, logs.String())
			})
		}
	}
}

func TestCloudMetamorphicAccountRefusalSecrecy(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "Example-Only-123")
	for _, status := range []int{400, 401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var logs bytes.Buffer
			c, err := NewFromEnvironment(Config{
				BaseURL: "https://cloud.example.invalid", AccountBaseURL: "https://account.example.invalid",
				Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
				Transport: transportFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"code":123,"msg":"reader@example.invalid Example-Only-123 synthetic-password-sentinel","data":{"accessToken":"synthetic-token-sentinel"}}`))}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			err = c.Login(t.Context())
			if err == nil {
				t.Fatal("account refusal installed a token")
			}
			assertCloudSecretSafe(t, err, logs.String())
		})
	}
}
