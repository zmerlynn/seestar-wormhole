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

func (c *credentialStore) rememberAt(ctx context.Context, s Settings, password string, generation uint64) error {
	if s.Email == "" {
		return &ActionError{Code: "email", Field: "email"}
	}
	if password != "" {
		if _, err := cloud.EmailCredentials(s.Email, password); err != nil {
			return actionError(err)
		}
	}
	c.mu.Lock()
	if c.generation != generation {
		c.mu.Unlock()
		return &ActionError{Code: "password", Field: "password"}
	}
	if password != "" {
		c.email, c.password = s.Email, password
	}
	email, p := c.email, c.password
	c.mu.Unlock()
	if email != s.Email || p == "" {
		return &ActionError{Code: "password", Field: "password"}
	}
	if len(email) > 1024 || len(p) > 1024 {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.generation != generation {
			return &ActionError{Code: "password", Field: "password"}
		}
		return c.outcomeLocked("password_save")
	}
	_, err := keychainCall(ctx, c, func() (string, error) {
		if c.generationID() != generation {
			return "", &ActionError{Code: "password", Field: "password"}
		}
		return "", c.chain.Set(keyringService, email, p)
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation {
		return &ActionError{Code: "password", Field: "password"}
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return c.outcomeLocked("keychain_timeout")
		}
		return c.outcomeLocked("password_save")
	}
	c.savedEmail = email
	c.code = "password_saved"
	c.notice = "Password saved to keychain. Checked when you connect."
	return nil
}
func (c *credentialStore) outcomeLocked(code string) error {
	c.code = code
	c.notice = errorCopy(code)
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
		if c.generation != generation {
			c.mu.Unlock()
			return cloud.Credentials{}, &ActionError{Code: "password", Field: "password"}
		}
		c.notice = "Reading saved password…"
		c.code = "password_reading"
		c.mu.Unlock()
		saved, err := keychainCall(ctx, c, func() (string, error) { return c.chain.Get(keyringService, s.Email) })
		if err != nil {
			if errors.Is(err, context.Canceled) {
				c.mu.Lock()
				if c.generation != generation {
					c.mu.Unlock()
					return cloud.Credentials{}, &ActionError{Code: "password", Field: "password"}
				}
				c.code = ""
				c.notice = "Saved in your keychain."
				c.mu.Unlock()
				return cloud.Credentials{}, err
			}
			code := "password_read"
			if errors.Is(err, keyring.ErrNotFound) {
				code = "password_missing"
			} else if errors.Is(err, context.DeadlineExceeded) {
				code = "keychain_timeout"
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.generation != generation {
				return cloud.Credentials{}, &ActionError{Code: "password", Field: "password"}
			}
			return cloud.Credentials{}, c.outcomeLocked(code)
		}
		p = saved
	}
	credentials, err := cloud.EmailCredentials(s.Email, p)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation {
		return cloud.Credentials{}, &ActionError{Code: "password", Field: "password"}
	}
	if err != nil {
		return cloud.Credentials{}, actionError(err)
	}
	c.email, c.password = s.Email, p
	c.code = ""
	c.notice = "Password kept until you quit."
	if s.Remember && password == "" {
		c.savedEmail = s.Email
		c.notice = "Saved in your keychain."
	}
	return credentials, nil
}
func (c *credentialStore) forgetAt(ctx context.Context, email string, generation uint64) error {
	c.mu.Lock()
	if c.generation != generation {
		c.mu.Unlock()
		return &ActionError{Code: "password", Field: "password"}
	}
	c.email, c.password = "", ""
	c.mu.Unlock()
	_, err := keychainCall(ctx, c, func() (string, error) {
		if c.generationID() != generation {
			return "", &ActionError{Code: "password", Field: "password"}
		}
		return "", c.chain.Delete(keyringService, email)
	})
	// Classify outside the memory mutex; provider errors may implement Is.
	missing := errors.Is(err, keyring.ErrNotFound)
	timeout := errors.Is(err, context.DeadlineExceeded)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation {
		return &ActionError{Code: "password", Field: "password"}
	}
	if err != nil && !missing {
		if timeout {
			return c.outcomeLocked("keychain_timeout")
		}
		return c.outcomeLocked("password_remove")
	}
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
	go func() {
		p, err := call()
		<-busy
		done <- result{p, err}
	}()
	select {
	case r := <-done:
		return r.password, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// HTTP commits carry the same identity as the native Save they acknowledge.
func (c *credentialStore) generationID() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}

// Each explicit mutation invalidates older provider results and disk commits.
func (c *credentialStore) nextGeneration() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	return c.generation
}
