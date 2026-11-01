package ui

import (
	"encoding/json/v2"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A failed settings write must still expose a saved password account.
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
