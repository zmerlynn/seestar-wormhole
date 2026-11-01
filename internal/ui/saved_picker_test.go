package ui

import (
	"context"
	"encoding/json/v2"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

func TestSavedPickerPage(t *testing.T) {
	for _, scenario := range []string{"boundary", "password", "retry", "failed", "deduplicate", "received saved pair"} {
		t.Run(scenario, func(t *testing.T) {
			b, _ := json.Marshal(scenario)
			runPageRegression(t, "saved-picker-test.js", "checkSavedPicker("+string(b)+")")
		})
	}
}

func TestPickerSettingsSaveBoundary(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure and retry"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := testServer(t)
				original := testSettings()
				original.Serial = "synthetic-old-1234"
				original.Model = "Old"
				original.Idle = "2m"
				if err := s.controller.persistLocked(original); err != nil {
					t.Fatal(err)
				}
				current := original
				current.Serial = "synthetic-new-5678"
				current.Model = "Future"
				read := func() Settings {
					t.Helper()
					w := httptest.NewRecorder()
					s.ServeHTTP(w, request(s, "GET", "/api/status", ""))
					var page struct {
						Settings Settings `json:"settings"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
						t.Fatal(err)
					}
					return page.Settings
				}
				check := func(want Settings, message string) {
					t.Helper()
					got := read()
					if got.Serial != want.Serial || got.Model != want.Model {
						t.Error(message)
					}
				}
				arm := make(chan struct{})
				s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status) error {
					<-arm
					status.MarkArmed()
					<-ctx.Done()
					return ctx.Err()
				}
				b, _ := json.Marshal(input{current, testPassword})
				if w := doPost(s, "/api/start", string(b)); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
				check(original, "accepted Connect replaced saved pair before boundary")
				path := filepath.Join(s.storage.root, "config.json")
				if fail {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
				close(arm)
				synctest.Wait()
				if fail {
					check(original, "failed boundary save replaced prior pair")
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if w := doPost(s, "/api/retry-storage", `{}`); w.Code != 200 {
						t.Fatal(w.Body.String())
					}
				}
				check(current, "successful save did not project current pair")
				s.controller.quit()
			})
		})
	}
}
