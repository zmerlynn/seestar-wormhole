package ui

import (
	"context"
	"encoding/json/v2"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChoicesDoNotCrossAccounts(t *testing.T) {
	for _, result := range []struct {
		name string
		err  error
	}{{"sign-in failure", cloud.ErrUnauthorized}, {"connected", nil}} {
		t.Run(result.name, func(t *testing.T) {
			s := testServer(t)
			failure := choiceFailure(t, cloudhttp.Device{})
			run := func(settings Settings) {
				t.Helper()
				if err := s.controller.start(settings, testPassword); err != nil {
					t.Fatal(err)
				}
				s.controller.mu.Lock()
				done := s.controller.done
				s.controller.mu.Unlock()
				if done != nil {
					<-done
				}
			}
			s.controller.run = func(context.Context, Settings, cloud.Credentials, *proxy.Status) error { return failure }
			original := testSettings()
			run(original)
			if len(s.controller.view().Devices) != 2 {
				t.Fatal("fixture did not retain choices")
			}
			s.controller.run = func(context.Context, Settings, cloud.Credentials, *proxy.Status) error { return result.err }
			normalized := original
			normalized.Email = " " + strings.ReplaceAll(original.Email, "@", " @") + " "
			run(normalized)
			if len(s.controller.view().Devices) != 2 {
				t.Error("normalized same-account list lost")
			}
			other := original
			other.Email = "other@example.invalid"
			run(other)
			if len(s.controller.view().Devices) != 0 {
				t.Error("previous account choices remain after changing account")
			}
			// A reopened controls page reads the same status projection.
			w := httptest.NewRecorder()
			s.ServeHTTP(w, request(s, "GET", "/api/status", ""))
			var reopened struct {
				Devices      []cloudhttp.DeviceChoice `json:"devices"`
				DevicesEmail string                   `json:"devices_email"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &reopened); err != nil {
				t.Fatal(err)
			}
			if reopened.DevicesEmail != other.Email {
				t.Error("status choices lack the current account identity")
			}
			if len(reopened.Devices) != 0 {
				t.Error("reopened controls retained another account's choices")
			}
		})
	}
}

func TestAccountPickerPage(t *testing.T) {
	runPageRegression(t, "account-picker-test.js", "checkAccountPicker()")
}
