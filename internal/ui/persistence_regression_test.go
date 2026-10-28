package ui

import (
	"context"
	"encoding/json/v2"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
)

func TestNewSaveSupersedesRunRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testServer(t)
		s.controller.save = s.saveSettings
		s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status) error {
			status.MarkArmed()
			<-ctx.Done()
			return ctx.Err()
		}
		if err := os.Mkdir(filepath.Join(s.storage.root, "config.json"), 0700); err != nil {
			t.Fatal(err)
		}
		original := testSettings()
		original.Idle = "2m"
		b, _ := json.Marshal(input{original, testPassword})
		if w := doPost(s, "/api/start", string(b)); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		synctest.Wait()
		if !s.controller.view().SaveFailed {
			t.Fatal("missing initial failure")
		}
		s.controller.stop()
		synctest.Wait()
		if err := os.Remove(filepath.Join(s.storage.root, "config.json")); err != nil {
			t.Fatal(err)
		}
		newer := original
		newer.Email = "newer@example.invalid"
		b, _ = json.Marshal(input{newer, testPassword})
		if w := doPost(s, "/api/password/save", string(b)); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		if s.controller.view().SaveFailed {
			t.Fatal("newer save left an obsolete run retry available")
		}
		first, err := loadSettings(s.storage)
		if err != nil || first.Email != newer.Email || !first.Remember {
			t.Fatal("new save failed")
		}
		if w := doPost(s, "/api/retry-storage", `{}`); w.Code != 200 && w.Code != 400 {
			t.Fatal(w.Body.String())
		}
		last, err := loadSettings(s.storage)
		if err != nil || last.Email != newer.Email || !last.Remember {
			t.Fatal("older run retry overwrote newer successful explicit save and association")
		}
	})
}

func TestStorageWriteDoesNotBlockStopOrStatus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testController(func(ctx context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status) error {
			status.MarkArmed()
			<-ctx.Done()
			return ctx.Err()
		})
		c.save = func(Settings) error {
			// A blocked disk write must leave this exact mutex available to Stop/status.
			if !c.mu.TryLock() {
				t.Error("settings storage holds the control mutex")
				return errStorage
			}
			c.mu.Unlock()
			c.stop()
			c.view()
			return nil
		}
		s := testSettings()
		s.Idle = "2m"
		if err := c.start(s, testPassword); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		c.quit()
	})
}
