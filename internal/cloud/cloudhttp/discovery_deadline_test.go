package cloudhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

type deadlineBlackhole struct{}

func (deadlineBlackhole) RoundTrip(r *http.Request) (*http.Response, error) {
	<-r.Context().Done()
	return nil, r.Context().Err()
}

type discoveryTransport struct {
	paths   []string
	entered chan struct{}
	delays  bool
}

func (f *discoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	path := r.URL.Path
	if path == "/v1/my-device" {
		path += "?current=" + r.URL.Query().Get("current")
	}
	f.paths = append(f.paths, path)
	var body string
	var delay time.Duration
	switch path {
	case "/v1/user/login":
		body = `{"code":0,"data":{"accessToken":"token-sentinel"}}`
		delay = 4 * time.Millisecond
	case "/v1/user/current-profile":
		body = `{"code":0,"data":{"id":"profile-sentinel"}}`
		delay = 3 * time.Millisecond
	case "/v1/my-device?current=1":
		body = `{"code":200,"msg":"ok","data":{"items":[{"deviceSn":"serial-one","deviceModel":"S50 Pro","ssid":"synthetic-wifi","deviceStatus":1}],"total":2}}`
		delay = 2 * time.Millisecond
	case "/v1/my-device?current=2":
		if f.entered != nil {
			close(f.entered)
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	default:
		return nil, errors.New("unexpected request, including allocation")
	}
	if f.delays {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-timer.C:
		}
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestDiscoveryLoginAndPagesShareDeadline(t *testing.T) {
	testDiscoveryAccount(t, true)
}

func TestDiscoveryCancelLaterPageJoins(t *testing.T) {
	testDiscoveryAccount(t, false)
}

func testDiscoveryAccount(t *testing.T, timeout bool) {
	t.Helper()
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")
	state := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		f := &discoveryTransport{delays: timeout, entered: make(chan struct{})}
		c, err := NewFromEnvironment(Config{Transport: f, StateDir: state, Timeout: 10 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		done := make(chan struct{})
		var selected Device
		go func() { defer close(done); selected, err = c.SelectDevice(ctx, Device{}) }()
		if !timeout {
			select {
			case <-f.entered:
			case <-done:
				t.Fatalf("discovery stopped before later page: %v; paths %v", err, f.paths)
			}
			cancel()
		}
		<-done
		c.Close() // This joins the account owner and its canceled HTTP exchange.
		select {
		case <-c.account.done:
		default:
			t.Fatal("account owner not joined")
		}
		wantErr := context.Canceled
		wantElapsed := time.Duration(0)
		if timeout {
			wantErr = context.DeadlineExceeded
			wantElapsed = 10 * time.Millisecond
		}
		if !errors.Is(err, wantErr) || selected != (Device{}) {
			t.Fatalf("selection = %v, error = %v; want empty selection and %v; paths %v", selected, err, wantErr, f.paths)
		}
		if elapsed := time.Since(start); elapsed != wantElapsed {
			t.Fatalf("discovery took %v; want %v", elapsed, wantElapsed)
		}
		wantPaths := []string{"/v1/user/login", "/v1/user/current-profile", "/v1/my-device?current=1", "/v1/my-device?current=2"}
		if !reflect.DeepEqual(f.paths, wantPaths) {
			t.Fatalf("requests = %v; want %v (no allocation)", f.paths, wantPaths)
		}
	})
}
func TestDiscoveryDeadline(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	synctest.Test(t, func(t *testing.T) {
		c, err := newAuthenticatedFixture(t, Config{Transport: deadlineBlackhole{}, Timeout: 10 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		_, err = c.SelectDevice(ctx, Device{})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v; want deadline exceeded", err)
		}
		if elapsed := time.Since(start); elapsed != 10*time.Millisecond {
			t.Fatalf("discovery took %v; want 10ms client timeout", elapsed)
		}
		if errors.Is(err, cloud.ErrConfig) {
			t.Fatal("deadline misclassified as configuration")
		}
	})
}
