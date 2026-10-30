package ui

import (
	"context"
	"encoding/json/v2"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/zalando/go-keyring"
)

// Pause Delete's result classification after the native gate is released.
// The newer Save can finish before the older Remove publishes or persists.
type pausedRemoval struct {
	once            sync.Once
	reached, resume chan struct{}
}

func (e *pausedRemoval) Error() string { return "synthetic missing entry" }
func (e *pausedRemoval) Is(target error) bool {
	e.once.Do(func() { close(e.reached); <-e.resume })
	return target == keyring.ErrNotFound
}

type publicationKeychain struct {
	fakeKeychain
	removal *pausedRemoval
}

func (f *publicationKeychain) Delete(s, u string) error {
	f.fakeKeychain.Delete(s, u)
	return f.removal
}
func TestNewSaveSupersedesRemovePublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := testSettings()
		pause := &pausedRemoval{reached: make(chan struct{}), resume: make(chan struct{})}
		chain := &publicationKeychain{removal: pause}
		c := &credentialStore{chain: chain, savedEmail: a.Email}
		removed := make(chan error, 1)
		go func() { removed <- c.forgetAt(context.Background(), a.Email, c.nextGeneration()) }()
		<-pause.reached
		if err := c.rememberAt(context.Background(), a, testPassword, c.nextGeneration()); err != nil {
			t.Fatal(err)
		}
		close(pause.resume)
		<-removed
		if p := c.state(a.Email); !p.Saved || p.Code != "password_saved" {
			t.Fatal("older Remove replaced newer Save", p)
		}
	})
}
func TestHTTPNewSaveSupersedesRemovePublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testServer(t)
		pause := &pausedRemoval{reached: make(chan struct{}), resume: make(chan struct{})}
		chain := &publicationKeychain{removal: pause}
		s.controller.credentials.chain = chain
		a := testSettings()
		body, _ := json.Marshal(input{a, testPassword})
		removed := make(chan struct{})
		go func() { doPost(s, "/api/forget", string(body)); close(removed) }()
		<-pause.reached
		a.Idle = "17m"
		body, _ = json.Marshal(input{a, testPassword})
		if w := doPost(s, "/api/password/save", string(body)); w.Code != 200 {
			t.Fatal("Save failed")
		}
		close(pause.resume)
		<-removed
		if p := s.controller.credentials.state(a.Email); !p.Saved || p.Code != "password_saved" {
			t.Error("older Remove replaced newer Save", p)
		}
		disk, err := loadSettings(s.storage)
		if err != nil || !disk.Remember || disk.Idle != a.Idle {
			t.Error("older Remove lost saved selection", err)
		}
	})
}
