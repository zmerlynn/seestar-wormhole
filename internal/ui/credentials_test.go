package ui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
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
	if err := c.remember(t.Context(), s, testPassword); err == nil {
		t.Fatal("keychain denial reported success")
	}
	if available, notice := c.info(); !available || !strings.Contains(notice, "for this launch") {
		t.Fatalf("%v %s", available, notice)
	}
	s.Remember = false
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
	if _, err := c.credentials(t.Context(), s, testPassword); err != nil {
		t.Fatal(err)
	}
	if chain.calls != 0 {
		t.Fatal("unchecked remember accessed keychain")
	}
	s.Remember = true
	if err := c.remember(t.Context(), s, ""); err != nil {
		t.Fatal(err)
	}
	if chain.calls != 1 || chain.value != testPassword {
		t.Fatal("explicit save did not save the cached password")
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

func TestForgetInvalidatesPendingGet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		chain := lockedKeychain{make(chan struct{}), make(chan struct{})}
		c := &credentialStore{chain: chain}
		s := testSettings()
		s.Remember = true
		got := make(chan error, 1)
		go func() { _, err := c.credentials(t.Context(), s, ""); got <- err }()
		<-chain.entered
		forgotten := make(chan struct{})
		go func() { c.forget(t.Context(), s.Email); close(forgotten) }()
		synctest.Wait() // Forget has cleared memory and is waiting for Get's native gate.
		close(chain.release)
		err := <-got
		<-forgotten // Delete has succeeded, and both callers have joined.
		if err == nil {
			t.Error("Start used a Get invalidated by Forget")
		}
		if available, notice := c.info(); available || notice != "Password removed from keychain and this app." {
			t.Errorf("after Forget: available=%v notice=%q", available, notice)
		}
		s.Remember = false
		if _, err := c.credentials(t.Context(), s, ""); err == nil {
			t.Fatal("later Start found a forgotten memory password")
		}
	})
}

func TestKeychainRecordSize(t *testing.T) {
	for _, tc := range []struct {
		name            string
		email, password string
		wantSet         bool
	}{
		{"email-boundary", strings.Repeat("a", 1014) + "@x.invalid", testPassword, true},
		{"email-oversize", strings.Repeat("a", 1015) + "@x.invalid", testPassword, false},
		{"password-boundary", testSettings().Email, strings.Repeat("a", 1024), true},
		{"password-oversize", testSettings().Email, strings.Repeat("a", 1025), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain := &fakeKeychain{}
			// A cache can also have been populated by a provider Get. Bound every Set,
			// even when a provider returned more than the credential validator accepts.
			c := &credentialStore{chain: chain, email: tc.email, password: tc.password}
			s := testSettings()
			s.Email, s.Remember = tc.email, true
			if err := c.remember(t.Context(), s, ""); (err == nil) != tc.wantSet {
				t.Fatalf("save error=%v, wantSet=%v", err, tc.wantSet)
			}
			if (chain.calls == 1) != tc.wantSet || chain.calls > 1 {
				t.Errorf("keychain calls=%d, want Set=%v", chain.calls, tc.wantSet)
			}
			if available, notice := c.info(); !available || (!tc.wantSet && !strings.Contains(notice, "for this launch")) {
				t.Errorf("available=%v notice=%q", available, notice)
			}
		})
	}
	// An oversized but valid email still permits Start with memory-only credentials.
	s := testSettings()
	s.Email, s.Remember = strings.Repeat("a", 1015)+"@x.invalid", true
	chain := &fakeKeychain{}
	c := &credentialStore{chain: chain}
	if _, err := c.credentials(t.Context(), s, testPassword); err != nil {
		t.Fatal(err)
	}
	if chain.calls != 0 {
		t.Fatal("Start sent oversized record to keychain")
	}
}

type readOnlyKeychain struct {
	t     *testing.T
	block <-chan struct{}
}

func (f readOnlyKeychain) Get(service, email string) (string, error) {
	if service != "seestar-wormhole" || email != testSettings().Email {
		f.t.Error("headless used a different keychain entry")
	}
	if f.block != nil {
		<-f.block
	}
	return testPassword, nil
}
func (f readOnlyKeychain) Set(string, string, string) error {
	f.t.Error("headless wrote keychain")
	return nil
}
func (f readOnlyKeychain) Delete(string, string) error {
	f.t.Error("headless deleted keychain")
	return nil
}

func TestHeadlessKeychainReadAndBound(t *testing.T) {
	p, err := keychainPassword(t.Context(), testSettings().Email, readOnlyKeychain{t: t})
	if err != nil || p != testPassword {
		t.Fatalf("read err=%v", err)
	}
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		defer close(release)
		start := time.Now()
		p, err := keychainPassword(t.Context(), testSettings().Email, readOnlyKeychain{t: t, block: release})
		if !errors.Is(err, context.DeadlineExceeded) || p != "" || time.Since(start) != 3*time.Second {
			t.Fatalf("bound: err=%v elapsed=%v", err, time.Since(start))
		}
	})
}
