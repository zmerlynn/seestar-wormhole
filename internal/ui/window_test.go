package ui

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// Exercise the real listener, private lock, activation request, and joined
// cleanup with a fake native window and keychain. No display or device is used.
func TestWindowLifetime(t *testing.T) {
	for _, trigger := range []string{"close", "quit", "signal", "startup failure"} {
		t.Run(trigger, func(t *testing.T) {
			root := t.TempDir() + "/state"
			process, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered, releasing, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			run := func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status) error {
				close(entered)
				<-ctx.Done()
				close(releasing)
				<-release
				return nil
			}
			err := runUIPresented(process, root, io.Discard, run, func(string) error { t.Error("opened browser"); return nil }, &fakeKeychain{}, func(ctx context.Context, url string, closeWindow func(), activate <-chan struct{}) error {
				if trigger == "startup failure" {
					return errStorage
				}
				parts := strings.Split(url, "/#")
				client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second}
				defer client.CloseIdleConnections()
				post := func(path, body string) {
					t.Helper()
					req, _ := http.NewRequest("POST", parts[0]+path, strings.NewReader(body))
					req.Header.Set("Origin", parts[0])
					req.Header.Set("X-Seestar-Token", parts[1])
					req.Header.Set("Content-Type", "application/json")
					resp, err := client.Do(req)
					if err != nil {
						t.Error(err)
						return
					}
					defer resp.Body.Close()
					if resp.StatusCode != 200 {
						t.Errorf("%s: %d", path, resp.StatusCode)
					}
				}
				// A second launch must activate the owner and return without opening a page.
				if err := runUIPresented(process, root, io.Discard, nil, func(string) error { t.Error("second launch opened browser"); return nil }, &fakeKeychain{}, func(context.Context, string, func(), <-chan struct{}) error { t.Error("second window"); return nil }, true); err != nil {
					t.Fatal(err)
				}
				select {
				case <-activate:
				case <-time.After(time.Second):
					t.Fatal("no activation")
				}
				body, _ := json.Marshal(input{Settings: testSettings(), Password: testPassword})
				post("/api/start", string(body))
				<-entered
				quitDone := make(chan struct{})
				switch trigger {
				case "close":
					closeWindow()
					closeWindow()
				case "signal":
					cancel()
				case "quit":
					go func() { post("/api/quit", "{}"); close(quitDone) }()
				}
				<-releasing
				select {
				case <-ctx.Done():
					t.Error("window closed before release joined")
				default:
				}
				close(release)
				select {
				case <-ctx.Done():
				case <-time.After(2 * time.Second):
					t.Fatal("cleanup did not finish")
				}
				if trigger == "quit" {
					<-quitDone
				}
				return nil
			}, true)
			if trigger == "startup failure" {
				if err != errStorage {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestActivationRequiresAuthentication(t *testing.T) {
	s := testServer(t)
	activated := false
	s.activate = func() { activated = true }
	for _, field := range []string{"token", "host", "origin", "method"} {
		r := request(s, "POST", "/api/activate", "{}")
		switch field {
		case "token":
			r.Header.Del("X-Seestar-Token")
		case "host":
			r.Host = "invalid.example"
		case "origin":
			r.Header.Set("Origin", "null")
		case "method":
			r.Method = "GET"
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code == 200 || activated {
			t.Fatal("activation admitted", field)
		}
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request(s, "POST", "/api/activate", "{}"))
	if w.Code != 200 || !activated {
		t.Fatal("activation refused")
	}
}

func TestWindowReceivesLaunchVersion(t *testing.T) {
	for _, injected := range []string{"", "synthetic-version"} {
		ctx := context.WithValue(t.Context(), versionKey{}, injected)
		want := injected
		if want == "" {
			want = "development"
		}
		err := runUIPresented(ctx, t.TempDir()+"/state", io.Discard, nil, func(string) error { t.Error("opened browser"); return nil }, &fakeKeychain{}, func(windowCtx context.Context, url string, quit func(), _ <-chan struct{}) error {
			if uiVersion(windowCtx) != want {
				t.Errorf("window version %q, want %q", uiVersion(windowCtx), want)
			}
			parts := strings.Split(url, "/#")
			req, _ := http.NewRequest("GET", parts[0]+"/api/status", nil)
			req.Header.Set("X-Seestar-Token", parts[1])
			client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
			defer client.CloseIdleConnections()
			resp, err := client.Do(req)
			if err != nil {
				t.Error(err)
			} else {
				defer resp.Body.Close()
				var status struct {
					Version string `json:"version"`
				}
				if err := json.UnmarshalRead(resp.Body, &status); err != nil || status.Version != want {
					t.Errorf("page version %q, want %q: %v", status.Version, want, err)
				}
			}
			quit()
			<-windowCtx.Done()
			return nil
		}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
}
