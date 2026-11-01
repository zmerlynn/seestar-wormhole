package ui

import (
	"context"
	"encoding/json/v2"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestChangeTelescope(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		for _, failSave := range []bool{false, true} {
			name := "success"
			if failSave {
				name = "save failure"
			}
			if uncertain {
				name += " uncertain release"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s := testServer(t)
					original := testSettings()
					original.Serial = "synthetic-change-1234"
					original.Model = "Future telescope"
					s.settings = original
					if err := s.saveSettings(original); err != nil {
						t.Fatal(err)
					}
					entered, release := make(chan struct{}, 2), make(chan struct{})
					s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status) error {
						status.SetSelectedDevice(cloudhttp.Device{Serial: original.Serial, Model: original.Model})
						entered <- struct{}{}
						<-ctx.Done()
						<-release
						if uncertain {
							return session.ErrStop
						}
						return ctx.Err()
					}
					if err := s.controller.start(original, testPassword); err != nil {
						t.Fatal(err)
					}
					<-entered
					if failSave {
						if err := os.Remove(filepath.Join(s.storage.root, "config.json")); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(filepath.Join(s.storage.root, "config.json"), 0700); err != nil {
							t.Fatal(err)
						}
					}
					response := make(chan *httptest.ResponseRecorder, 1)
					go func() { response <- doPost(s, "/api/change-telescope", `{}`) }()
					synctest.Wait()
					view := s.controller.view()
					if !view.Active || !view.Stopping || view.Status != "Stopping — releasing the Seestar session…" {
						t.Fatal("change did not stop the session", view)
					}
					select {
					case <-response:
						t.Fatal("change returned before release joined")
					default:
					}
					if s.settings.Serial != original.Serial || s.settings.Model != original.Model {
						t.Fatal("cleared before release")
					}
					if err := s.controller.start(original, testPassword); err == nil {
						t.Fatal("start accepted during change")
					}
					close(release)
					w := <-response
					if s.controller.view().Active || s.controller.view().SelectedDevice != "" {
						t.Fatal("session or identity survived change")
					}
					if failSave {
						if w.Code != 400 {
							t.Fatal("save failure accepted", w.Code)
						}
						var failure struct {
							Code    string `json:"code"`
							Message string `json:"message"`
						}
						if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
							t.Fatal(err)
						}
						if failure.Code != "storage" || failure.Message != "Settings could not be saved; they may reset next time." {
							t.Fatal("save failure not reported", w.Body.String())
						}
						if s.settings.Serial != original.Serial || s.settings.Model != original.Model {
							t.Fatal("failed save cleared fields")
						}
						if !s.controller.view().SaveFailed {
							t.Fatal("failed save lost retry")
						}
						if s.controller.view().Status == "Stopped. Choose Connect to pick a telescope." {
							t.Fatal("failed clear announced completion")
						}
						if retry := doPost(s, "/api/retry-storage", `{}`); retry.Code != 400 {
							t.Fatal("failed retry accepted", retry.Code)
						}
						if err := os.Remove(filepath.Join(s.storage.root, "config.json")); err != nil {
							t.Fatal(err)
						}
						if retry := doPost(s, "/api/retry-storage", `{}`); retry.Code != 200 {
							t.Fatal("retry failed", retry.Body.String())
						}
					} else if w.Code != 200 {
						t.Fatal(w.Body.String())
					}
					saved, err := loadSettings(s.storage)
					if err != nil {
						t.Fatal(err)
					}
					if saved.Serial != "" || saved.Model != "" || s.settings.Serial != "" || s.settings.Model != "" {
						t.Fatal("selection not cleared and persisted")
					}
					if saved.Email != original.Email || saved.Bind != original.Bind || saved.Idle != original.Idle {
						t.Fatal("unrelated settings changed")
					}
					if s.controller.view().SaveFailed {
						t.Fatal("successful save left retry")
					}
					wantStatus := "Stopped. Choose Connect to pick a telescope."
					if uncertain {
						wantStatus = errorCopy("release")
					}
					if got := s.controller.view().Status; got != wantStatus {
						t.Fatalf("change status: got %q want %q", got, wantStatus)
					}
					if s.controller.changing {
						t.Fatal("change remained busy")
					}
				})
			})
		}
	}
}

func TestChangeTelescopeRequiresSelection(t *testing.T) {
	s := testServer(t)
	for _, running := range []bool{false, true} {
		if running {
			s.controller.done = make(chan struct{})
			s.controller.status = proxy.NewStatus()
		}
		if w := doPost(s, "/api/change-telescope", `{}`); w.Code != 400 {
			t.Fatal("change without selected telescope accepted")
		}
	}
}

func TestChangeTelescopeControlBinding(t *testing.T) {
	html, _ := assets.ReadFile("web/index.html")
	if !strings.Contains(string(html), `<div class="telescope-status"><span id="change-telescope-group" class="change-telescope-group" hidden><button id="change-telescope" type="button" aria-describedby="change-telescope-help" hidden>Change telescope</button><span id="change-telescope-help" class="help" hidden>Stops the session.</span></span></div>`) {
		t.Fatal("Change telescope control not beside selected status")
	}
	js, _ := assets.ReadFile("web/app.js")
	if !strings.Contains(string(js), `['change-telescope','change-telescope']`) {
		t.Fatal("Change telescope action not wired")
	}
}

// Failed selection clears retain their pair, but account projection must still
// expose a successful native password save when its settings write fails.
func TestFailedSaveKeepsPasswordAccountProjection(t *testing.T) {
	s := testServer(t)
	s.settings.Email = ""
	if err := os.Mkdir(filepath.Join(s.storage.root, "config.json"), 0700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(input{testSettings(), testPassword})
	if w := doPost(s, "/api/password/save", string(body)); w.Code != 400 {
		t.Fatal("storage failure accepted")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, request(s, "GET", "/api/status", ""))
	var page struct {
		Settings Settings     `json:"settings"`
		Password passwordInfo `json:"password"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Settings.Email != testSettings().Email || !page.Password.Saved || !page.Password.Memory {
		t.Fatal("failed settings write hid the saved password account", w.Body.String())
	}
}

func TestChangeChoosingProjection(t *testing.T) {
	for _, scenario := range []string{"received", "other account", "none", "uncertain", "failed save"} {
		t.Run(scenario, func(t *testing.T) {
			s := testServer(t)
			c := s.controller
			pair := testSettings()
			c.devicesEmail = pair.Email
			if scenario != "none" {
				c.devices = []cloudhttp.DeviceChoice{{Serial: "synthetic-choice", Model: "S30"}}
			}
			if scenario == "other account" {
				c.devicesEmail = "other@example.invalid"
			}
			if scenario == "uncertain" {
				c.failure = &ActionError{Code: "release"}
				c.final = errorCopy("release")
			}
			if scenario == "failed save" {
				if err := os.Mkdir(filepath.Join(s.storage.root, "config.json"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := c.persistTelescopeClearLocked(pair)
			if (err != nil) != (scenario == "failed save") {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, request(s, "GET", "/api/status", ""))
			var v struct {
				Choosing bool   `json:"choosing"`
				Status   string `json:"status"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
			want := scenario == "received" || scenario == "uncertain"
			if v.Choosing != want {
				t.Fatalf("choosing: got %v want %v", v.Choosing, want)
			}
			if scenario == "received" && v.Status != "Stopped. Choose a telescope, then Connect." {
				t.Fatal("retained-choice status", v.Status)
			}
			if scenario == "uncertain" && v.Status != errorCopy("release") {
				t.Fatal("release precedence", v.Status)
			}
			if scenario == "none" || scenario == "other account" {
				if v.Status != "Stopped. Choose Connect to pick a telescope." {
					t.Fatal("fetch status", v.Status)
				}
			}
			if scenario == "received" {
				if err := c.start(pair, testPassword); err != nil {
					t.Fatal(err)
				}
				c.quit()
				if c.view().Choosing {
					t.Fatal("Connect retained choosing state")
				}
			}
		})
	}
}
