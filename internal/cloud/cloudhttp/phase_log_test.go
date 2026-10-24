package cloudhttp

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountPhaseLogs(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "Example-Only-123")
	for _, reject := range []bool{false, true} {
		var logs bytes.Buffer
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/user/login":
				if reject {
					w.WriteHeader(401)
					io.WriteString(w, `{"msg":"Example-Only-123"}`)
					return
				}
				io.WriteString(w, `{"code":0,"data":{"accessToken":"token-sentinel"}}`)
			case "/v1/user/current-profile":
				io.WriteString(w, `{"code":0,"data":{"id":"profile-sentinel"}}`)
			case "/v1/my-device/detail":
				io.WriteString(w, detail)
			case "/v1/device-p2p/online-link":
				io.WriteString(w, online)
			case "/v1/device-p2p/stop":
				io.WriteString(w, `{"code":200,"msg":"ok","data":null}`)
			default:
				t.Error("unexpected request")
			}
		}))
		c, err := NewFromEnvironment(Config{Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), BaseURL: server.URL, AccountBaseURL: server.URL, StateDir: t.TempDir() + "/.live-state", Transport: server.Client().Transport})
		if err != nil {
			t.Fatal(err)
		}
		d := Device{Serial: "serial", Model: "S50 Pro"}
		_, err = c.Detail(t.Context(), d)
		if err == nil {
			var lease *Lease
			lease, err = c.Acquire(t.Context(), d)
			if err == nil {
				err = lease.Close()
			}
		}
		c.Close()
		server.Close()
		if reject {
			if err == nil || !strings.Contains(logs.String(), `"phase":"login","reason":"unauthorized"`) {
				t.Fatalf("missing rejection: %v %s", err, &logs)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			for _, phase := range []string{"login", "account_profile", "device_detail", "allocation", "release"} {
				for _, state := range []string{"started", "ready"} {
					if !strings.Contains(logs.String(), `"phase":"`+phase+`","state":"`+state+`"`) {
						t.Fatalf("missing %s %s in %s", phase, state, &logs)
					}
				}
			}
		}
		for _, secret := range []string{"reader@example.invalid", "Example-Only-123", "token-sentinel", "profile-sentinel", "fixture-password"} {
			if strings.Contains(logs.String(), secret) {
				t.Fatal("secret in logs")
			}
		}
	}
}
