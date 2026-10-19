package cloudhttp

import (
	"bytes"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestUnknownDetailDiagnostic(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "Fixture-Only-123")
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		var b bytes.Buffer
		body := `{"code":200,"name-secret":{},"data":{"deviceSn":"serial-secret","deviceModel":"model-secret","deviceStatus":99,"key-secret":"value-secret","ssid":"synthetic-wifi"}}`
		c, err := NewFromEnvironment(Config{BaseURL: "https://fixture.invalid", AccountBaseURL: "https://account.fixture.invalid",
			Logger: slog.New(slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: level})), Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				response := body
				if r.URL.Path == "/v1/user/login" {
					response = `{"data":{"accessToken":"token-secret"}}`
				}
				if r.URL.Path == "/v1/user/current-profile" {
					response = `{}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response))}, nil
			})})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Close)
		unknown := body
		body = `{"code":200,"data":{"deviceSn":"other","deviceModel":"model-secret","deviceStatus":99,"ssid":"synthetic-wifi"}}`
		if _, err := c.Detail(t.Context(), Device{Serial: "serial-secret", Model: "model-secret"}); err == nil {
			t.Fatal("unknown status bypassed identity")
		}
		body = `{"code":200,"name-secret":{},"data":{"deviceSn":"serial-secret","deviceModel":"model-secret","deviceStatus":1,"ssid":"synthetic-wifi"}}`
		if _, err := c.Detail(t.Context(), Device{Serial: "serial-secret", Model: "model-secret"}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.String(), "cloud_receive_oddity") {
			t.Fatal("known or uncorrelated status logged as tolerated")
		}
		body = unknown
		for range 100 {
			d, err := c.Detail(t.Context(), Device{Serial: "serial-secret", Model: "model-secret"})
			if err != nil || d.Status() != 99 {
				t.Fatalf("unknown status changed: status=%d err=%v", d.Status(), err)
			}
		}
		count := 0
		decoder := jsontext.NewDecoder(strings.NewReader(b.String()))
		for decoder.PeekKind() != 0 {
			var event struct {
				Message  string `json:"msg"`
				Level    string `json:"level"`
				Category string `json:"category"`
			}
			if err := json.UnmarshalDecode(decoder, &event, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatal(err)
			}
			if event.Message == "cloud_receive_oddity" {
				count++
				if event.Category != "unknown_device_status" || event.Level != "DEBUG" {
					t.Fatal("wrong cloud oddity category")
				}
			}
		}
		want := 64
		if level == slog.LevelInfo {
			want = 0
		}
		if count != want {
			t.Fatalf("oddities=%d want=%d", count, want)
		}
		if strings.Contains(b.String(), "secret") {
			t.Fatal("cloud unknown names/values escaped")
		}
	}
}
