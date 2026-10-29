package ui

import (
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"testing"
)

type lockedKeychain struct{ entered, release chan struct{} }

func (f lockedKeychain) Get(string, string) (string, error) {
	close(f.entered)
	<-f.release
	return testPassword, nil
}
func (f lockedKeychain) Set(string, string, string) error { return nil }
func (f lockedKeychain) Delete(string, string) error      { return nil }
func TestStopDuringKeychainStartup(t *testing.T) {
	chain := lockedKeychain{make(chan struct{}), make(chan struct{})}
	defer close(chain.release)
	c := testController(func(context.Context, Settings, cloud.Credentials, *proxy.Status) error {
		t.Error("canceled startup ran proxy")
		return nil
	})
	c.credentials.chain = chain
	s := testSettings()
	s.Remember = true
	if err := c.start(s, ""); err != nil {
		t.Fatal(err)
	}
	<-chain.entered
	c.quit()
	if text, active := c.snapshot(); active || text != "Stopped. Seestar may still be releasing the session." {
		t.Fatalf("%s %v", text, active)
	}
}
