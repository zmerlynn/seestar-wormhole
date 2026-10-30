package ui

import (
	"context"
	"encoding/json/v2"
	"net/http/httptest"
	"testing"
	"testing/synctest"
)

// Later Save must agree with the provider after an older pending Remove.
func TestReviewNewSaveAfterPendingRemove(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testSettings()
		f := &orderedKeychain{values: map[string]string{s.Email: testPassword}, entered: make(chan struct{}), release: make(chan struct{}), blockDelete: true}
		c := &credentialStore{chain: f, savedEmail: s.Email}
		removed, saved := make(chan error, 1), make(chan error, 1)
		go func() { removed <- c.forgetAt(context.Background(), s.Email, c.nextGeneration()) }()
		<-f.entered
		go func() { saved <- c.rememberAt(context.Background(), s, testPassword, c.nextGeneration()) }()
		synctest.Wait()
		close(f.release)
		<-removed
		if err := <-saved; err != nil {
			t.Fatal("later Save failed", err)
		}
		synctest.Wait()
		f.mu.Lock()
		exists := f.values[s.Email] != ""
		f.mu.Unlock()
		if !exists {
			t.Fatal("provider did not save")
		}
		if p := c.state(s.Email); !p.Saved || p.Code != "password_saved" {
			t.Fatal("older Remove overrode newer completed Save", p.Saved, p.Code)
		}
	})
}

func TestReviewHTTPNewSaveAfterPendingRemove(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testServer(t)
		a := testSettings()
		f := &orderedKeychain{values: map[string]string{a.Email: testPassword}, entered: make(chan struct{}), release: make(chan struct{}), blockDelete: true}
		s.controller.credentials = &credentialStore{chain: f, savedEmail: a.Email}
		removed, saved := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
		body, _ := json.Marshal(input{a, testPassword})
		go func() { removed <- doPost(s, "/api/forget", string(body)) }()
		<-f.entered
		go func() { saved <- doPost(s, "/api/password/save", string(body)) }()
		synctest.Wait()
		close(f.release)
		<-removed
		if w := <-saved; w.Code != 200 {
			t.Fatal("later Save failed", w.Code)
		}
		synctest.Wait()
		f.mu.Lock()
		exists := f.values[a.Email] != ""
		f.mu.Unlock()
		if !exists {
			t.Fatal("provider did not save")
		}
		p := s.controller.credentials.state(a.Email)
		if !p.Saved || p.Code != "password_saved" {
			t.Error("older Remove overrode newer completed Save", p.Saved, p.Code)
		}
		got, err := loadSettings(s.storage)
		if err != nil || !got.Remember {
			t.Error("saved selection lost on disk", err)
		}
	})
}
