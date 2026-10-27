package ui

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testPassword = "synthetic-password"

type fakeKeychain struct {
	mu    sync.Mutex
	value string
	err   error
	calls int
}

func (f *fakeKeychain) Get(string, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.value, f.err
}
func (f *fakeKeychain) Set(_, _, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err == nil {
		f.value = p
	}
	return f.err
}
func (f *fakeKeychain) Delete(string, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err == nil {
		f.value = ""
	}
	return f.err
}
func TestKeychainUnavailableMemoryOnly(t *testing.T) {
	st := testStorage(t)
	chain := &fakeKeychain{err: errors.New("private backend error " + testPassword)}
	c := &credentialStore{chain: chain}
	s := testSettings()
	s.Remember = true
	if err := c.remember(t.Context(), s, testPassword); err != nil {
		t.Fatal(err)
	}
	if available, notice := c.info(); !available || !strings.Contains(notice, "kept in memory") {
		t.Fatalf("%v %s", available, notice)
	}
	if _, err := c.credentials(t.Context(), s, ""); err != nil {
		t.Fatal("memory fallback unavailable", err)
	}
	if err := st.write("config.json", s); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(st.root, "config.json"))
	if bytes.Contains(b, []byte(testPassword)) {
		t.Fatal("password persisted")
	}
	c.forget(t.Context(), s.Email)
	if available, _ := c.info(); available {
		t.Fatal("Forget retained memory password")
	}
	if chain.value != "" {
		t.Fatal("unavailable backend wrote password")
	}
}
func TestKeychainOptInAndGet(t *testing.T) {
	chain := &fakeKeychain{}
	c := &credentialStore{chain: chain}
	s := testSettings()
	if err := c.remember(t.Context(), s, testPassword); err != nil {
		t.Fatal(err)
	}
	if chain.calls != 0 {
		t.Fatal("unchecked remember accessed keychain")
	}
	s.Remember = true
	if err := c.remember(t.Context(), s, ""); err != nil {
		t.Fatal(err)
	}
	loaded := &credentialStore{chain: chain}
	if _, err := loaded.credentials(t.Context(), s, ""); err != nil {
		t.Fatal(err)
	}
	loaded.forget(t.Context(), s.Email)
	if chain.value != "" {
		t.Fatal("Forget did not delete")
	}
}
