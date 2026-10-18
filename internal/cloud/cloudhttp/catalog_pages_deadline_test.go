package cloudhttp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type catalogDeadlineTransport struct {
	pages     selectionCloud
	paths     []string
	deadlines []time.Time
	entered   chan struct{}
}

func (f *catalogDeadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	path := r.URL.Path
	if path == "/v1/my-device" {
		path += "?" + r.URL.RawQuery
	}
	f.paths = append(f.paths, path)
	deadline, _ := r.Context().Deadline()
	f.deadlines = append(f.deadlines, deadline)
	if r.URL.Query().Get("current") == "3" {
		close(f.entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	timer := time.NewTimer(2 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return nil, r.Context().Err()
	case <-timer.C:
	}
	return f.pages.RoundTrip(r)
}

func TestCatalogPageDeadlineAndCancellation(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")
	for _, timeout := range []bool{false, true} {
		for _, input := range []Device{{}, {Serial: "private-a"}} {
			t.Run(fmtCatalogDeadlineName(timeout, input), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					f := &catalogDeadlineTransport{pages: selectionCloud{bodies: []string{pageA3, pageA3}}, entered: make(chan struct{})}
					var logs bytes.Buffer
					c, err := NewFromEnvironment(Config{Transport: f, Timeout: 10 * time.Millisecond, StateDir: t.TempDir(), Logger: redactionTestLogger(&logs, slog.LevelDebug)})
					if err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					start := time.Now()
					done := make(chan struct{})
					var got Device
					go func() { defer close(done); got, err = c.SelectDevice(ctx, input) }()
					select {
					case <-f.entered:
					case <-done:
						t.Fatalf("discovery stopped before third page: %v", err)
					}
					if !timeout {
						cancel()
					}
					<-done
					c.Close()
					wantErr := context.Canceled
					wantElapsed := 8 * time.Millisecond
					if timeout {
						wantErr = context.DeadlineExceeded
						wantElapsed = 10 * time.Millisecond
					}
					if !errors.Is(err, wantErr) || got != (Device{}) {
						t.Fatalf("selection=%v error=%v, want empty/%v", got, err, wantErr)
					}
					if elapsed := time.Since(start); elapsed != wantElapsed {
						t.Fatalf("elapsed=%v, want %v", elapsed, wantElapsed)
					}
					wantPaths := []string{"/v1/user/login", "/v1/user/current-profile", "/v1/my-device?current=1&pageSize=100", "/v1/my-device?current=2&pageSize=100", "/v1/my-device?current=3&pageSize=100"}
					if !reflect.DeepEqual(f.paths, wantPaths) {
						t.Fatalf("paths=%v, want %v (no allocation)", f.paths, wantPaths)
					}
					for _, deadline := range f.deadlines {
						if deadline != start.Add(10*time.Millisecond) {
							t.Fatalf("request deadline=%v, want single discovery deadline %v", deadline, start.Add(10*time.Millisecond))
						}
					}
					select {
					case <-c.account.done:
					default:
						t.Fatal("account owner not joined")
					}
					if strings.Contains(logs.String(), "device_selected") {
						t.Fatal("selected from incomplete catalog")
					}
					assertCatalogPagePrivacy(t, logs.String()+err.Error())
				})
			})
		}
	}
}

func fmtCatalogDeadlineName(timeout bool, input Device) string {
	name := "cancel"
	if timeout {
		name = "timeout"
	}
	if input.Serial != "" {
		name += "/explicit"
	} else {
		name += "/automatic"
	}
	return name
}
