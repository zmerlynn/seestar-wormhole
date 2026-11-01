package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

type choicesTransport func(*http.Request) (*http.Response, error)

func (f choicesTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDeviceChoicesCLIOutput(t *testing.T) {
	setValidCommandEnvironment(t)
	t.Setenv("SEESTAR_EMAIL", "synthetic@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "synthetic-password")
	c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{StateDir: t.TempDir(), Transport: choicesTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"data":{"items":[{"deviceSn":"synthetic-choice-1234","deviceModel":"S30","deviceStatus":1,"ssid":"synthetic"},{"deviceSn":"synthetic-choice-5678","deviceModel":"Seestar S50 Pro","deviceStatus":1,"ssid":"synthetic"}],"total":2}}`
		if strings.HasSuffix(r.URL.Path, "/login") {
			body = `{"data":{"accessToken":"synthetic-token"}}`
		}
		if strings.HasSuffix(r.URL.Path, "/current-profile") {
			body = `{"data":{"id":"synthetic-profile"}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, selection := c.SelectDevice(t.Context(), cloudhttp.Device{})
	var typed *cloudhttp.SelectionError
	if !errors.As(selection, &typed) {
		t.Fatal("fixture did not enumerate")
	}
	var out, logs bytes.Buffer
	err = runWithListener(t.Context(), []string{"proxy", "--bind=127.0.0.1", "--port=13459:13459"}, &out, &logs, func(context.Context, proxy.Config) (commandSession, error) { return nil, selection }, func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") })
	if err == nil {
		t.Fatal("selection failure lost")
	}
	if out.String() != selection.Error()+"\n" {
		t.Fatalf("CLI lost choice error: %q", out.String())
	}
	for _, line := range []string{"\nS30 synthetic-choice-1234\n", "\nSeestar S50 Pro synthetic-choice-5678\n"} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("CLI missing copyable pair %q", line)
		}
	}
	if strings.Contains(logs.String(), "synthetic-choice-") || strings.Contains(logs.String(), "S50 Pro") {
		t.Fatal("catalog identity in structured logs")
	}
	if !strings.Contains(logs.String(), `"reason":"invalid_option"`) {
		t.Fatal("selection lost config classification")
	}
}
