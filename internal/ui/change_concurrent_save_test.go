package ui

import (
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"testing"
	"testing/synctest"
)

func TestChangePreservesConcurrentSave(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testServer(t)
		original := testSettings()
		original.Serial = "synthetic-old-1234"
		original.Model = "Old"
		s.settings = original
		if err := s.saveSettings(original); err != nil {
			t.Fatal(err)
		}
		entered, release := make(chan struct{}), make(chan struct{})
		s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status) error {
			status.SetSelectedDevice(cloudhttp.Device{Serial: original.Serial, Model: original.Model})
			close(entered)
			<-ctx.Done()
			<-release
			return ctx.Err()
		}
		if err := s.controller.start(original, testPassword); err != nil {
			t.Fatal(err)
		}
		<-entered
		changed := make(chan error, 1)
		go func() { changed <- s.controller.changeTelescope() }()
		synctest.Wait()
		latest := original
		latest.Idle = "7m"
		if err := s.controller.persistPassword(latest, s.controller.credentials.generationID()); err != nil {
			t.Fatal(err)
		}
		close(release)
		if err := <-changed; err != nil {
			t.Fatal(err)
		}
		saved, err := loadSettings(s.storage)
		if err != nil {
			t.Fatal(err)
		}
		if saved.Idle != latest.Idle {
			t.Error("Change telescope overwrote successfully saved unrelated settings during release", saved.Idle)
		}
		if saved.Serial != "" || saved.Model != "" {
			t.Error("Change telescope did not clear the latest pair")
		}
	})
}
