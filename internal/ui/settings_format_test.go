package ui

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
)

func TestSettingsVersionOneFixture(t *testing.T) {
	st := testStorage(t)
	body := []byte(`{
  "version":1,"bind":"0.0.0.0","discovery":true,"idle":"1h30m",
  "serial":"SYNTHETIC_SERIAL","model":"S50",
  "ports":[{"local":1445,"device":445},{"local":0,"device":80}],
  "email":"synthetic@example.invalid","remember_password":true
}`)
	if err := st.writeBytes("config.json", body); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSettings(st.root)
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{Version: 1, Bind: "0.0.0.0", Discovery: true, Idle: "1h30m", Serial: "SYNTHETIC_SERIAL", Model: "S50", Ports: []proxyrun.Port{{Local: 1445, Device: 445}, {Local: 0, Device: 80}}, Email: "synthetic@example.invalid", Remember: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("settings = %+v; want %+v", got, want)
	}
	_, idle, err := got.validate()
	if err != nil || idle != 90*time.Minute {
		t.Fatalf("idle = %v, error = %v", idle, err)
	}
	after, err := os.ReadFile(filepath.Join(st.root, "config.json"))
	if err != nil || !bytes.Equal(after, body) {
		t.Fatalf("read rewrote settings: %s, %v", after, err)
	}
}

func TestSettingsAbsentMemberDefaults(t *testing.T) {
	st := testStorage(t)
	if err := st.writeBytes("config.json", []byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	got, err := loadSettings(st)
	want := Settings{Version: 1, Bind: "127.0.0.1", Idle: "0", Ports: []proxyrun.Port{}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("settings = %+v, %v; want %+v", got, err, want)
	}
	if defaultSettings().Version != 1 {
		t.Fatal("first launch must write version 1")
	}
}

func TestSettingsFutureMembersSurviveConnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testServer(t)
		body := []byte(`{"version":1,"email":"synthetic@example.invalid","idle":"2m","future_object":{"enabled":true,"limit":17},"future_array":[1,"two",{"three":null}],"future_flag":false}`)
		if err := s.storage.writeBytes("config.json", body); err != nil {
			t.Fatal(err)
		}
		var err error
		s.settings, err = loadSettings(s.storage)
		if err != nil {
			t.Fatal(err)
		}
		s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status) error {
			status.MarkArmed()
			<-ctx.Done()
			return ctx.Err()
		}
		defer s.controller.quit()
		// Like the page, submit known members only, with an edited idle time.
		request := `{"settings":{"version":1,"bind":"127.0.0.1","idle":"3m","email":"synthetic@example.invalid"},"password":"synthetic-password"}`
		if w := doPost(s, "/api/start", request); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		synctest.Wait()
		saved, err := os.ReadFile(filepath.Join(s.storage.root, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var got, original map[string]any
		if err := json.Unmarshal(saved, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &original); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"future_object", "future_array", "future_flag"} {
			if value, ok := got[key]; !ok || !reflect.DeepEqual(value, original[key]) {
				t.Errorf("lost or changed %s: %s", key, saved)
			}
		}
		if got["idle"] != "3m" || got["version"] != float64(1) {
			t.Fatalf("Connect edits missing: %s", saved)
		}
	})
}

func TestSettingsPortKeys(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       proxyrun.Port
		valid      bool
	}{
		{"lowercase", `{"version":1,"idle":"0","ports":[{"local":80,"device":80}]}`, proxyrun.Port{Local: 80, Device: 80}, true},
		{"uppercase", `{"version":1,"idle":"0","ports":[{"Local":80}]}`, proxyrun.Port{}, false},
		{"uppercase_local", `{"version":1,"idle":"0","ports":[{"Local":80,"device":80}]}`, proxyrun.Port{Device: 80}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Settings{Bind: "127.0.0.1", Idle: "0"}
			if err := json.Unmarshal([]byte(tc.body), &got); err != nil {
				t.Fatal(err)
			}
			if len(got.Ports) != 1 || got.Ports[0].Local != tc.want.Local || got.Ports[0].Device != tc.want.Device {
				t.Fatalf("ports = %+v; want %+v", got.Ports, tc.want)
			}
			_, _, err := got.validate()
			if tc.valid && err != nil || !tc.valid && actionError(err).Code != "ports" {
				t.Fatalf("validation = %v", err)
			}
			encoded, err := json.Marshal(tc.want)
			if err != nil || !bytes.Contains(encoded, []byte(`"local":`)) || !bytes.Contains(encoded, []byte(`"device":`)) {
				t.Fatalf("port keys = %s, %v", encoded, err)
			}
		})
	}
}

func TestSettingsRefusedFileCannotBeSaved(t *testing.T) {
	for _, body := range []string{`{"version":2,"future":{"keep":[1,2]}}`, `{"future":{"keep":[1,2]}}`} {
		t.Run(body, func(t *testing.T) {
			s := testServer(t)
			if err := s.storage.writeBytes("config.json", []byte(body)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			t.Chdir(filepath.Dir(s.storage.root))
			err := runUIPresented(ctx, filepath.Base(s.storage.root), io.Discard, nil, func(string) error {
				t.Error("opened UI for refused settings")
				return nil
			}, &fakeKeychain{}, nil, false)
			if err == nil || err.Error() != "Could not load saved settings safely.\n\n"+filepath.Join(s.storage.root, "config.json")+"\n\nCheck this file, then reopen seestar-wormhole." {
				t.Errorf("startup did not explain refusal: %v", err)
			}
			if _, err := loadSettings(s.storage); err == nil || actionError(err).Code != "settings" {
				t.Errorf("expected invalid-settings error, got %v", err)
			}
			// Connect, password save/removal, and retry share this save callback.
			if err := s.saveSettings(defaultSettings()); err == nil {
				t.Error("save after refusal succeeded")
			}
			// Also cover the lower-level atomic replacement path.
			if err := s.storage.writeBytes("config.json", []byte(`{"version":1}`)); err == nil {
				t.Error("raw save after refusal succeeded")
			}
			after, err := os.ReadFile(filepath.Join(s.storage.root, "config.json"))
			if err != nil || string(after) != body {
				t.Fatalf("refused file changed: %s, %v", after, err)
			}
		})
	}
}
