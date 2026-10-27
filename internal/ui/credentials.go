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
	mu              sync.Mutex
	generation      uint64
	keyBusy         chan struct{}
	chain           keychain
	email, password string
	notice          string
}

func (c *credentialStore) remember(ctx context.Context, s Settings, password string) error {
	if password != "" {
		if _, err := cloud.EmailCredentials(s.Email, password); err != nil {
			return errSettings
		}
		c.mu.Lock()
		c.email, c.password = s.Email, password
		c.mu.Unlock()
	}
	if !s.Remember {
		c.mu.Lock()
		c.notice = "Password kept in memory for this process"
		c.mu.Unlock()
		return nil
	}
	c.mu.Lock()
	email, p := c.email, c.password
	c.mu.Unlock()
	if email != s.Email || p == "" {
		return nil
	}
	// Keep records well below Darwin's 4096-byte command limit, including
	// password encoding and command overhead. Use the same bound on every OS.
	if len(email) > 1024 || len(p) > 1024 {
		c.mu.Lock()
		c.notice = "Email or password too large for OS keychain; password kept in memory"
		c.mu.Unlock()
		return nil
	}
	_, err := keychainCall(ctx, c, func() (string, error) { return "", c.chain.Set(keyringService, email, p) })
	c.mu.Lock()
	defer c.mu.Unlock()
	if errors.Is(err, context.DeadlineExceeded) {
		c.notice = "OS keychain did not respond; password kept in memory. The pending OS request may still finish"
	} else if err != nil {
		c.notice = "OS keychain unavailable or locked; password kept in memory"
	} else {
		c.notice = "Password saved in OS keychain"
	}
	return nil
}
func (c *credentialStore) credentials(ctx context.Context, s Settings, password string) (cloud.Credentials, error) {
	if err := c.remember(ctx, s, password); err != nil {
		return cloud.Credentials{}, err
	}
	c.mu.Lock()
	email, p, generation := c.email, c.password, c.generation
	c.mu.Unlock()
	if email != s.Email {
		p = ""
	}
	if p == "" && s.Remember {
		saved, err := keychainCall(ctx, c, func() (string, error) { return c.chain.Get(keyringService, s.Email) })
		if err == nil {
			c.mu.Lock()
			if c.generation == generation {
				p = saved
				c.email, c.password = s.Email, p
			}
			c.mu.Unlock()
		} else {
			c.mu.Lock()
			c.notice = "OS keychain unavailable or locked; enter a password for this process"
			c.mu.Unlock()
		}
	}
	return cloud.EmailCredentials(s.Email, p)
}
func (c *credentialStore) forget(ctx context.Context, email string) {
	c.mu.Lock()
	c.generation++
	c.email, c.password = "", ""
	c.mu.Unlock()
	_, err := keychainCall(ctx, c, func() (string, error) { return "", c.chain.Delete(keyringService, email) })
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil && err != keyring.ErrNotFound {
		c.notice = "Memory password forgotten; OS keychain unavailable or locked — retry Forget to remove its copy"
	} else {
		c.notice = "Password forgotten"
	}
}
func (c *credentialStore) info() (bool, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.password != "", c.notice
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
