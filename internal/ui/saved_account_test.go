package ui

import (
	"context"
	"encoding/json/v2"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

// Exercise the actual HTTP projections, then reopen a page from each response.
func TestSavedPairAccountAssociation(t *testing.T) {
	for _, route := range []string{"start", "failed save"} {
		for _, same := range []bool{false, true} {
			name := "different account"
			if same {
				name = "normalized same account"
			}
			t.Run(route+"/"+name, func(t *testing.T) {
				s := testServer(t)
				first := testSettings()
				first.Email = " first @example.invalid "
				first.Serial, first.Model = "synthetic-first-account-1234", "Future one"
				if err := s.controller.persistLocked(first); err != nil {
					t.Fatal(err)
				}
				next := first
				next.Email = "first@example.invalid"
				if !same {
					next.Email = "second@example.invalid"
				}
				next.Serial, next.Model = "synthetic-second-pair-5678", "Future two"
				if route == "start" {
					next.Serial, next.Model = "", ""
				}
				submitted := next
				submitted.Email = " " + strings.ReplaceAll(next.Email, "@", " @") + " "
				body, err := json.Marshal(input{submitted, testPassword})
				if err != nil {
					t.Fatal(err)
				}
				check := func(want Settings, failed bool, code string) {
					t.Helper()
					w := httptest.NewRecorder()
					s.ServeHTTP(w, request(s, "GET", "/api/status", ""))
					var page struct {
						Settings   Settings     `json:"settings"`
						SaveFailed bool         `json:"save_failed"`
						Password   passwordInfo `json:"password"`
						Failure    *ActionError `json:"failure"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
						t.Fatal(err)
					}
					if page.Settings.Email != want.Email || page.Settings.Serial != want.Serial || page.Settings.Model != want.Model {
						t.Errorf("projected account/pair: got %q/%q/%q want %q/%q/%q", page.Settings.Email, page.Settings.Serial, page.Settings.Model, want.Email, want.Serial, want.Model)
					}
					if page.SaveFailed != failed {
						t.Error("save retry state lost")
					}
					if code == "password_saved" {
						if !page.Password.Saved || page.Password.Code != code {
							t.Error("account password-save feedback lost")
						}
					} else if page.Failure == nil || page.Failure.Code != code {
						t.Error("sign-in failure feedback lost")
					}
					encoded, err := json.Marshal(want)
					if err != nil {
						t.Fatal(err)
					}
					runPageRegression(t, "reopened-account-test.js", "checkReopenedAccount("+w.Body.String()+","+string(encoded)+")")
				}
				want := next
				want.Serial, want.Model = "", ""
				if same {
					want.Serial, want.Model = first.Serial, first.Model
				}
				if route == "start" {
					s.controller.run = func(context.Context, Settings, cloud.Credentials, *proxy.Status) error { return cloud.ErrUnauthorized }
					if w := doPost(s, "/api/start", string(body)); w.Code != 200 {
						t.Fatal(w.Body.String())
					}
					s.controller.mu.Lock()
					done := s.controller.done
					s.controller.mu.Unlock()
					if done != nil {
						<-done
					}
					check(want, false, "signin")
				} else {
					path := filepath.Join(s.storage.root, "config.json")
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
					if w := doPost(s, "/api/password/save", string(body)); w.Code != 400 {
						t.Fatal("fixture save did not fail", w.Body.String())
					}
					check(want, true, "password_saved")
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if w := doPost(s, "/api/retry-storage", `{}`); w.Code != 200 {
						t.Fatal(w.Body.String())
					}
					check(next, false, "password_saved")
				}
			})
		}
	}
}
