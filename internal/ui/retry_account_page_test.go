package ui

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// Feed the real failed-save and retry projections to the complete page script.
func TestRetrySettingsAccountDraft(t *testing.T) {
	s := testServer(t)
	original := testSettings()
	original.Email = "first@example.invalid"
	original.Serial, original.Model = "synthetic-first-1234", "First model"
	if err := s.controller.persistLocked(original); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.storage.root, "config.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	next := original
	next.Serial, next.Model = "synthetic-first-5678", "Saved first model"
	body, err := json.Marshal(input{next, testPassword})
	if err != nil {
		t.Fatal(err)
	}
	if w := doPost(s, "/api/password/save", string(body)); w.Code != http.StatusBadRequest {
		t.Fatal("failed-save fixture did not fail", w.Code)
	}
	read := func() string {
		t.Helper()
		w := httptest.NewRecorder()
		s.ServeHTTP(w, request(s, "GET", "/api/status", ""))
		if w.Code != http.StatusOK {
			t.Fatal(w.Code)
		}
		return w.Body.String()
	}
	before := read()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// The page sends its current draft, but retry must save the retained snapshot.
	draft := original
	draft.Email, draft.Serial, draft.Model = "second@example.invalid", "", ""
	body, err = json.Marshal(input{Settings: draft})
	if err != nil {
		t.Fatal(err)
	}
	if w := doPost(s, "/api/retry-storage", string(body)); w.Code != http.StatusOK {
		t.Fatal("retry failed", w.Code)
	}
	after := read()
	next.Remember = true
	if saved, err := loadSettings(s.storage); err != nil || !reflect.DeepEqual(saved, next) {
		t.Fatalf("retry did not save its controller-owned snapshot: got %+v, err %v", saved, err)
	}
	for _, account := range []string{"different", "same", "normalized owner"} {
		for _, manual := range []bool{false, true} {
			t.Run(account+"/manual="+strconv.FormatBool(manual), func(t *testing.T) {
				call := "checkRetryAccountDraft(" + before + "," + after + "," + strconv.Quote(account) + "," + strconv.FormatBool(manual) + ")"
				runPageRegression(t, "retry-account-test.js", call)
			})
		}
	}
}
