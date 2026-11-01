package ui

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

type choiceTransport func(*http.Request) (*http.Response, error)

func (f choiceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func choiceFailure(t *testing.T, input cloudhttp.Device) error {
	t.Helper()
	t.Setenv("SEESTAR_EMAIL", "synthetic@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "synthetic-password")
	c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{StateDir: t.TempDir(), Transport: choiceTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"data":{"items":[{"deviceSn":"synthetic-choice-1234","deviceModel":"S30","deviceStatus":1,"ssid":"synthetic"},{"deviceSn":"synthetic-choice-5678","deviceModel":" <img>\t ","deviceStatus":1,"ssid":"synthetic"}],"total":2}}`
		if input.Serial == "synthetic-choice-1234" {
			body = strings.Replace(body, `"total":2`, `"total":0`, 1)
		}
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
	_, err = c.SelectDevice(t.Context(), input)
	if err == nil {
		t.Fatal("expected selection failure")
	}
	return err
}
func TestDeviceChoicesUIAPI(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		count   int
	}{
		{"multiple", choiceFailure(t, cloudhttp.Device{}), 2},
		{"missing serial", choiceFailure(t, cloudhttp.Device{Serial: "absent"}), 0},
		{"incomplete with serial", choiceFailure(t, cloudhttp.Device{Serial: "synthetic-choice-1234"}), 0},
		{"other selection", diagnostic.Wrap("device_selection", "fixed", cloud.ErrConfig), 0},
		{"auth", cloud.ErrUnauthorized, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			s.controller.log = &eventLog{storage: s.storage}
			release := make(chan struct{})
			s.controller.run = func(context.Context, Settings, cloud.Credentials, *proxy.Status) error { <-release; return tc.failure }
			if err := s.controller.start(testSettings(), testPassword); err != nil {
				t.Fatal(err)
			}
			s.controller.mu.Lock()
			done := s.controller.done
			s.controller.mu.Unlock()
			close(release)
			<-done
			w := httptest.NewRecorder()
			s.ServeHTTP(w, request(s, "GET", "/api/status", ""))
			var got struct {
				Status  string `json:"status"`
				Failure struct {
					Field   string                   `json:"field"`
					Code    string                   `json:"code"`
					Devices []cloudhttp.DeviceChoice `json:"devices"`
				} `json:"failure"`
				Effective diagnosticSettings `json:"effective"`
				Events    []event            `json:"events"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
				t.Fatalf("status %d: %v", w.Code, err)
			}
			if len(got.Failure.Devices) != tc.count {
				t.Fatalf("choices: %s", w.Body.String())
			}
			if tc.count > 0 {
				if got.Status != "Choose a telescope" || got.Failure.Field != "" {
					t.Fatal("selection step presented as an error", w.Body.String())
				}
				if got.Failure.Code != "selection_multiple" || got.Failure.Devices[1].Serial != "synthetic-choice-5678" || got.Failure.Devices[1].Model != " <img>\t " || got.Failure.Devices[1].Label != ` <img>\t  synthetic-choice-5678` {
					t.Fatalf("wire or display identity: %s", w.Body.String())
				}
				if len(got.Events) == 0 || got.Events[len(got.Events)-1].Code != "selection_multiple" {
					t.Fatal("multiple-device recovery event lost")
				}
			} else if strings.Contains(w.Body.String(), `"devices"`) {
				t.Fatal("choices on unrelated failure")
			}
			disk, err := os.ReadFile(filepath.Join(s.storage.root, "gui.log"))
			if err != nil || strings.Contains(string(disk), "synthetic-choice-") || strings.Contains(string(disk), "<img>") {
				t.Fatal("full identity in GUI log", err)
			}
			summary, _ := json.Marshal(struct {
				Settings diagnosticSettings
				Events   []event
			}{got.Effective, got.Events})
			if strings.Contains(string(summary), "synthetic-choice-") || strings.Contains(string(summary), "<img>") {
				t.Fatal("full identity in copied summary")
			}
		})
	}
}

func TestDevicePickerBindings(t *testing.T) {
	html, _ := assets.ReadFile("web/index.html")
	for _, want := range []string{`<div id="device-picker">`, `<label for="device-choice">Telescope</label>`, `<select id="device-choice" aria-describedby="selection-help"></select>`, `<p id="selection-help" class="help" hidden>Your account has several telescopes. Choose one, then Connect.</p>`} {
		if !strings.Contains(string(html), want) {
			t.Fatalf("picker binding missing: %s", want)
		}
	}
	if got := errorCopy("selection_multiple"); got != "Your account has several telescopes. Choose one, then Connect." {
		t.Fatal(got)
	}
}

func TestSavedPickerChoiceLabel(t *testing.T) {
	s := testSettings()
	s.Serial = "synthetic-\u200b-望-choice"
	s.Model = "Future\tmodel"
	got := pageSettingsFor(s)
	if got.ChoiceLabel != `Future\tmodel synthetic-\u200b-\u671b-choice` || got.Serial != s.Serial || got.Model != s.Model {
		t.Fatalf("saved picker label or wire pair: %#v", got)
	}
}

func TestPickerChoicesAppLifetime(t *testing.T) {
	s := testServer(t)
	failure := choiceFailure(t, cloudhttp.Device{})
	s.controller.run = func(context.Context, Settings, cloud.Credentials, *proxy.Status) error { return failure }
	run := func() {
		t.Helper()
		if err := s.controller.start(testSettings(), testPassword); err != nil {
			t.Fatal(err)
		}
		s.controller.mu.Lock()
		done := s.controller.done
		s.controller.mu.Unlock()
		if done != nil {
			<-done
		}
	}
	run()
	choices := s.controller.view().Devices
	if len(choices) != 2 || choices[1].Serial != "synthetic-choice-5678" {
		t.Fatal("received list not retained", choices)
	}
	s.controller.run = func(context.Context, Settings, cloud.Credentials, *proxy.Status) error { return nil }
	run()
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request(s, "GET", "/api/status", ""))
	var view struct {
		Devices []cloudhttp.DeviceChoice `json:"devices"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Devices) != 2 || view.Devices[1] != choices[1] {
		t.Fatal("list lost after next run or controls reload", w.Body.String())
	}
	if err := s.controller.persistLocked(testSettings()); err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(filepath.Join(s.storage.root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(disk), "synthetic-choice-") || strings.Contains(string(disk), "devices") || strings.Contains(string(disk), "choice_label") {
		t.Fatal("picker metadata persisted", string(disk))
	}
}
