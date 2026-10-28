package ui

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func testController(run runFunc) *controller {
	return &controller{parent: context.Background(), run: run, credentials: &credentialStore{chain: &fakeKeychain{}}}
}
func TestControllerLifetimes(t *testing.T) {
	entered := make(chan struct{}, 2)
	cleanup := make(chan struct{}, 2)
	release := make(chan struct{})
	c := testController(func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status) error {
		entered <- struct{}{}
		<-ctx.Done()
		cleanup <- struct{}{}
		<-release
		return nil
	})
	s := testSettings()
	if err := c.start(s, testPassword); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := c.start(s, testPassword); err == nil {
		t.Fatal("repeated Start accepted")
	}
	c.stop()
	<-cleanup
	if text, active := c.snapshot(); !active || text != "Stopping — releasing the telescope session…" {
		t.Fatalf("%s %v", text, active)
	}
	if err := c.start(s, testPassword); err == nil {
		t.Fatal("Start before cleanup joined")
	}
	joined := make(chan struct{})
	go func() { c.quit(); close(joined) }()
	select {
	case <-joined:
		t.Fatal("Quit did not join cleanup")
	default:
	}
	close(release)
	<-joined
	if text, active := c.snapshot(); active || text != "Stopped" {
		t.Fatalf("%s %v", text, active)
	}
	if err := c.start(s, testPassword); err == nil {
		t.Fatal("Start after Quit")
	}
}
func TestControllerStopStartDuringStartup(t *testing.T) {
	entered := make(chan struct{}, 2)
	c := testController(func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status) error {
		entered <- struct{}{}
		<-ctx.Done()
		return nil
	})
	for range 2 {
		if err := c.start(testSettings(), testPassword); err != nil {
			t.Fatal(err)
		}
		<-entered
		c.mu.Lock()
		done := c.done
		c.mu.Unlock()
		c.stop()
		<-done
		if text, active := c.snapshot(); active || text != "Stopped" {
			t.Fatalf("%s %v", text, active)
		}
	}
	c.quit()
}
func testServer(t *testing.T) *server {
	t.Helper()
	s := &server{host: "127.0.0.1:12345", token: strings.Repeat("a", 64), storage: testStorage(t), controller: testController(func(context.Context, Settings, cloud.Credentials, *proxy.Status) error {
		return errors.New(testPassword)
	}), settings: testSettings(), quit: make(chan struct{})}
	s.controller.save = s.saveSettings
	s.controller.lastSaved = s.settings
	return s
}
func request(s *server, method, path, body string) *http.Request {
	r := httptest.NewRequest(method, "http://"+s.host+path, strings.NewReader(body))
	r.Header.Set("X-Seestar-Token", s.token)
	r.Header.Set("Origin", "http://"+s.host)
	r.Header.Set("Content-Type", "application/json")
	return r
}
func TestHTTPRejections(t *testing.T) {
	s := testServer(t)
	for _, tc := range []struct {
		name, method, path string
		mutate             func(*http.Request)
		code               int
	}{
		{"token", "GET", "/api/status", func(r *http.Request) { r.Header.Del("X-Seestar-Token") }, 403},
		{"wrong token", "GET", "/api/status", func(r *http.Request) { r.Header.Set("X-Seestar-Token", strings.Repeat("b", 64)) }, 403},
		{"host", "GET", "/", func(r *http.Request) { r.Host = "localhost:12345"; r.Header.Set("X-Forwarded-Host", s.host) }, 403},
		{"api host", "GET", "/api/status", func(r *http.Request) { r.Host = "rebind.invalid" }, 403},
		{"origin missing", "POST", "/api/stop", func(r *http.Request) { r.Header.Del("Origin") }, 403},
		{"origin null", "POST", "/api/stop", func(r *http.Request) { r.Header.Set("Origin", "null") }, 403},
		{"origin foreign", "POST", "/api/stop", func(r *http.Request) { r.Header.Set("Origin", "https://example.invalid") }, 403},
		{"fetch metadata", "POST", "/api/stop", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
		{"content type", "POST", "/api/stop", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		{"method", "GET", "/api/stop", nil, 405},
		{"status post", "POST", "/api/status", nil, 405},
		{"malformed", "POST", "/api/start", func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("{")) }, 400},
		{"oversize", "POST", "/api/save", func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", maxJSON+1))) }, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request(s, tc.method, tc.path, "{}")
			if tc.mutate != nil {
				tc.mutate(r)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("%d want %d", w.Code, tc.code)
			}
		})
	}
	for _, action := range []string{"start", "stop", "save", "forget", "quit"} {
		for _, method := range []string{"GET", "HEAD", "PUT", "DELETE"} {
			w := httptest.NewRecorder()
			s.ServeHTTP(w, request(s, method, "/api/"+action, "{}"))
			if w.Code != 405 {
				t.Fatalf("%s %s = %d", method, action, w.Code)
			}
		}
	}
	select {
	case <-s.quit:
		t.Fatal("rejected request quit")
	default:
	}
}
func TestHTTPNoSecretsAndHeaders(t *testing.T) {
	s := testServer(t)
	for _, path := range []string{"/", "/app.js", "/style.css", "/icon.svg", "/api/status", "/api/instance"} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, request(s, "GET", path, ""))
		if w.Code != 200 {
			t.Fatalf("%s %d", path, w.Code)
		}
		for k, want := range map[string]string{"X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer", "Cache-Control": "no-store"} {
			if w.Header().Get(k) != want {
				t.Fatalf("%s %s", path, k)
			}
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatal("CSP")
		}
	}
	in, _ := json.Marshal(input{testSettings(), testPassword})
	for _, action := range []string{"password/save", "start"} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, request(s, "POST", "/api/"+action, string(in)))
		if w.Code != 200 {
			t.Fatalf("%s %s", action, w.Body)
		}
		if strings.Contains(w.Body.String(), testPassword) {
			t.Fatal("password in response")
		}
	}
	s.controller.mu.Lock()
	done := s.controller.done
	s.controller.mu.Unlock()
	if done != nil {
		<-done
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request(s, "GET", "/api/status", ""))
	if strings.Contains(w.Body.String(), testPassword) || strings.Contains(w.Body.String(), s.token) {
		t.Fatal("secret in status")
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, request(s, "POST", "/api/quit", "{}"))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	select {
	case <-s.quit:
	default:
		t.Fatal("Quit did not signal")
	}
}
func TestFinalStatus(t *testing.T) {
	if got := finalStatus(errors.New(testPassword)); got != "Could not connect. Check your network and try again." {
		t.Errorf("unexpected failure guidance: %q", got)
	}
	if got := finalStatus(errors.New(testPassword)); strings.Contains(got, testPassword) {
		t.Fatal("raw error leaked")
	}
}

// No browser or real keychain is used, and every listener is 127.0.0.1:0.
func TestDoubleLaunchAndStaleRecord(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	st, err := openStorage(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.write("ui-runtime.json", runtimeRecord{12345, strings.Repeat("b", 64)}); err != nil {
		t.Fatal(err)
	}
	st.directory.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	urls := make(chan string, 4)
	opener := func(url string) error { urls <- url; return nil }
	var runs int
	run := func(context.Context, Settings, cloud.Credentials, *proxy.Status) error { runs++; return nil }
	done := make(chan error, 1)
	go func() { done <- runUI(ctx, root, io.Discard, run, opener, &fakeKeychain{}) }()
	var first string
	select {
	case first = <-urls:
	case <-time.After(5 * time.Second):
		t.Fatal("startup timeout")
	}
	if strings.Contains(first, strings.Repeat("b", 64)) {
		t.Fatal("stale record reused")
	}
	if err := runUI(t.Context(), root, io.Discard, run, opener, &fakeKeychain{}); err != nil {
		t.Fatal(err)
	}
	if second := <-urls; second != first {
		t.Fatal("second launch did not reopen existing page")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatal("loading settings started proxy")
	}
	if _, err := os.Stat(filepath.Join(root, bootstrapName)); !os.IsNotExist(err) {
		t.Fatal("bootstrap remained")
	}
	if _, err := os.Stat(filepath.Join(root, "ui-runtime.json")); !os.IsNotExist(err) {
		t.Fatal("runtime record remained")
	}
}

func TestTerminalRejectionCancelsAndJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rejected := make(chan struct{}, 1)
	cleanup := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runObserved(cancel, rejected, func() error { <-ctx.Done(); close(cleanup); <-release; return cloud.ErrConfig })
	}()
	rejected <- struct{}{}
	<-cleanup
	select {
	case <-done:
		t.Fatal("terminal rejection skipped cleanup join")
	default:
	}
	close(release)
	if got := finalStatus(<-done); got != "Sign-in was rejected. Check your Seestar account email and password, then connect again." {
		t.Fatal(got)
	}
}

func TestHTTPQuitJoinsCleanup(t *testing.T) {
	s := testServer(t)
	entered, cleanup, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.controller = testController(func(ctx context.Context, _ Settings, _ cloud.Credentials, _ *proxy.Status) error {
		close(entered)
		<-ctx.Done()
		close(cleanup)
		<-release
		return nil
	})
	if err := s.controller.start(testSettings(), testPassword); err != nil {
		t.Fatal(err)
	}
	<-entered
	done := make(chan struct{})
	w := httptest.NewRecorder()
	go func() { s.ServeHTTP(w, request(s, "POST", "/api/quit", "{}")); close(done) }()
	<-cleanup
	select {
	case <-done:
		t.Fatal("Quit responded before cleanup joined")
	default:
	}
	close(release)
	<-done
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if _, active := s.controller.snapshot(); active {
		t.Fatal("Quit left active run")
	}
}

func TestControllerFinalCleanupError(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed-release", true: "canceled-and-failed-release"}[canceled], func(t *testing.T) {
			outcome := errors.Join(session.ErrStop, errors.New(testPassword))
			if canceled {
				outcome = errors.Join(context.Canceled, outcome)
			}
			complete := make(chan struct{})
			c := testController(func(context.Context, Settings, cloud.Credentials, *proxy.Status) error {
				<-complete
				return outcome
			})
			if err := c.start(testSettings(), testPassword); err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			done := c.done
			c.mu.Unlock()
			close(complete)
			<-done
			text, active := c.snapshot()
			if active || text != "Stopped, but session release could not be confirmed. Wait 60 seconds before connecting again." {
				t.Fatalf("completed controller: text=%q active=%v", text, active)
			}
		})
	}
}
