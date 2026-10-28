package ui

import (
	"context"
	"github.com/zalando/go-keyring"
	"sync"
	"testing"
	"testing/synctest"
)

type orderedKeychain struct {
	mu                    sync.Mutex
	values                map[string]string
	entered, release      chan struct{}
	blockSet, blockDelete bool
	target                string
}

func (f *orderedKeychain) Get(_ string, email string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.values[email]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return p, nil
}
func (f *orderedKeychain) Set(_ string, email, p string) error {
	if f.blockSet {
		f.target = email
		close(f.entered)
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[email] = p
	return nil
}
func (f *orderedKeychain) Delete(_ string, email string) error {
	if f.blockDelete {
		f.target = email
		close(f.entered)
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.values, email)
	return nil
}

func TestRemoveSupersedesEarlierPendingSave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := testSettings()
		f := &orderedKeychain{values: map[string]string{}, entered: make(chan struct{}), release: make(chan struct{}), blockSet: true}
		c := &credentialStore{chain: f}
		saved := make(chan error, 1)
		removed := make(chan error, 1)
		go func() { saved <- c.remember(context.Background(), a, testPassword) }()
		<-f.entered
		go func() { removed <- c.forget(context.Background(), a.Email) }()
		synctest.Wait()
		close(f.release)
		<-saved // A superseded Save may correctly return an error.
		if err := <-removed; err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		f.mu.Lock()
		p := f.values[a.Email]
		f.mu.Unlock()
		if p != "" {
			t.Fatal("provider still has removed secret")
		}
		if c.state(a.Email).Saved {
			t.Fatal("older Save completion resurrected association after newer Remove")
		}
	})
}
