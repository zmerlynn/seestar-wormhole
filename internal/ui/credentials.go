package ui

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

const keyringService = "seestar-wormhole"

type keychain interface {
	Get(string, string) (string, error)
	Set(string, string, string) error
	Delete(string, string) error
}
type osKeychain struct{}

func (osKeychain) Get(s, u string) (string, error) { return keyring.Get(s, u) }
func (osKeychain) Set(s, u, p string) error        { return keyring.Set(s, u, p) }
func (osKeychain) Delete(s, u string) error        { return keyring.Delete(s, u) }

// KeychainPassword reads the same service/account entry as the UI, with the
// same bounded wait. It never saves or deletes credentials.
func KeychainPassword(ctx context.Context, email string) (string, error) {
	return keychainPassword(ctx, email, osKeychain{})
}

func keychainPassword(ctx context.Context, email string, chain keychain) (string, error) {
	c := &credentialStore{chain: chain}
	return keychainCall(ctx, c, func() (string, error) { return chain.Get(keyringService, email) })
}

// The store never exposes the password. Keychain calls run outside the memory
// mutex, so a locked provider cannot block status, Stop, or Quit.
type credentialStore struct {
	mu               sync.Mutex
	generation       uint64
	keyBusy          chan struct{}
	chain            keychain
	email, password  string
	notice           string
	code, savedEmail string
}

// remember is used only by the explicit Save password action.
func (c *credentialStore) remember(ctx context.Context, s Settings, password string) error {
	if s.Email == "" {
		return &ActionError{Code: "email", Field: "email"}
	}
	if password != "" {
		if _, err := cloud.EmailCredentials(s.Email, password); err != nil {
			return &ActionError{Code: "password", Field: "password"}
		}
		c.mu.Lock()
		c.email, c.password = s.Email, password
		c.mu.Unlock()
	}
	c.mu.Lock()
	email, p := c.email, c.password
	c.mu.Unlock()
	if email != s.Email || p == "" {
		return &ActionError{Code: "password", Field: "password"}
	}
	if len(email) > 1024 || len(p) > 1024 {
		return c.outcome("password_save")
	}
	_, err := keychainCall(ctx, c, func() (string, error) { return "", c.chain.Set(keyringService, email, p) })
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return c.outcome("keychain_timeout")
		}
		return c.outcome("password_save")
	}
	c.mu.Lock()
	c.savedEmail = email
	c.code = "password_saved"
	c.notice = "Password saved to keychain. It will be checked when you connect."
	c.mu.Unlock()
	return nil
}
func (c *credentialStore) outcome(code string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.code = code
	switch code {
	case "password_save":
		c.notice = "Could not save to keychain. You can connect using this password for this launch."
	case "password_remove":
		c.notice = "Password cleared from this app. Could not remove the keychain copy. Unlock your keychain and choose Retry removal."
	case "password_missing":
		c.notice = "The saved password was not found. Enter it again."
	case "password_read":
		c.notice = "Could not read the saved password. Unlock your keychain and try again, or enter a password for this launch."
	case "keychain_timeout":
		c.notice = "The keychain has not responded. Its request may still finish. You can enter a password for this launch."
	}
	return &ActionError{Code: code, Field: "password"}
}
func (c *credentialStore) credentials(ctx context.Context, s Settings, password string) (cloud.Credentials, error) {
	c.mu.Lock()
	// A changed account never inherits another account's in-memory credential.
	if c.email != s.Email {
		c.email, c.password = "", ""
	}
	generation := c.generation
	p := c.password
	c.mu.Unlock()
	if password != "" {
		p = password
	} else if s.Remember {
		c.mu.Lock()
		c.notice = "Reading saved password…"
		c.code = "password_reading"
		c.mu.Unlock()
		saved, err := keychainCall(ctx, c, func() (string, error) { return c.chain.Get(keyringService, s.Email) })
		if err != nil {
			if errors.Is(err, context.Canceled) {
				c.mu.Lock()
				c.code = ""
				c.notice = "A saved password is selected. Your OS may ask for permission when you connect."
				c.mu.Unlock()
				return cloud.Credentials{}, err
			}
			code := "password_read"
			if errors.Is(err, keyring.ErrNotFound) {
				code = "password_missing"
			} else if errors.Is(err, context.DeadlineExceeded) {
				code = "keychain_timeout"
			}
			return cloud.Credentials{}, c.outcome(code)
		}
		p = saved
	}
	credentials, err := cloud.EmailCredentials(s.Email, p)
	if err != nil {
		return cloud.Credentials{}, &ActionError{Code: "password", Field: "password"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation {
		return cloud.Credentials{}, &ActionError{Code: "password", Field: "password"}
	}
	c.email, c.password = s.Email, p
	c.code = ""
	c.notice = "Password available for this launch only."
	if s.Remember && password == "" {
		c.savedEmail = s.Email
		c.notice = "A saved password is selected. Your OS may ask for permission when you connect."
	}
	return credentials, nil
}
func (c *credentialStore) forget(ctx context.Context, email string) error {
	c.mu.Lock()
	c.generation++
	c.email, c.password = "", ""
	c.mu.Unlock()
	_, err := keychainCall(ctx, c, func() (string, error) { return "", c.chain.Delete(keyringService, email) })
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		if errors.Is(err, context.DeadlineExceeded) {
			return c.outcome("keychain_timeout")
		}
		return c.outcome("password_remove")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.savedEmail == email {
		c.savedEmail = ""
	}
	c.code = "password_removed"
	c.notice = "Password removed from keychain and this app."
	return nil
}
func (c *credentialStore) info() (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.password != "", c.notice
}

type passwordInfo struct {
	Saved  bool   `json:"saved"`
	Memory bool   `json:"memory"`
	Busy   bool   `json:"busy"`
	Code   string `json:"code"`
	Notice string `json:"notice"`
}

func (c *credentialStore) state(email string) passwordInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return passwordInfo{c.savedEmail == email && email != "", c.email == email && c.password != "", len(c.keyBusy) > 0, c.code, c.notice}
}
func (c *credentialStore) association(s Settings) Settings {
	c.mu.Lock()
	defer c.mu.Unlock()
	s.Remember = c.savedEmail == s.Email && s.Email != ""
	return s
}

// The backend has no context API. Bound the caller's wait and serialize native
// operations, including a Forget queued behind a still-running Set. Backend
// work owns no session and cannot hold up proxy cleanup or process shutdown.
func keychainCall(ctx context.Context, c *credentialStore, call func() (string, error)) (string, error) {
	c.mu.Lock()
	if c.keyBusy == nil {
		c.keyBusy = make(chan struct{}, 1)
	}
	busy := c.keyBusy
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	select {
	case busy <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	type result struct {
		password string
		err      error
	}
	done := make(chan result, 1)
	go func() { defer func() { <-busy }(); p, err := call(); done <- result{p, err} }()
	select {
	case r := <-done:
		return r.password, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
