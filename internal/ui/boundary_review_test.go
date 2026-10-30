package ui

import (
	"context"
	"encoding/json/v2"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"testing"
	"testing/synctest"
)

func TestReviewLaterExplicitSaveBeforeRunBoundary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := testServer(t)
		arm := make(chan struct{})
		s.controller.run = func(ctx context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status) error {
			<-arm
			status.MarkArmed()
			<-ctx.Done()
			return ctx.Err()
		}
		old := testSettings()
		old.Idle = "2m"
		body, _ := json.Marshal(input{old, testPassword})
		if w := doPost(s, "/api/start", string(body)); w.Code != 200 {
			t.Fatal("Start failed")
		}
		synctest.Wait()
		newer := old
		newer.Idle = "17m"
		body, _ = json.Marshal(input{newer, testPassword})
		if w := doPost(s, "/api/password/save", string(body)); w.Code != 200 {
			t.Fatal("explicit Save failed")
		}
		close(arm)
		synctest.Wait()
		got, err := loadSettings(s.storage)
		s.controller.stop()
		synctest.Wait()
		s.controller.quit()
		if err != nil {
			t.Fatal(err)
		}
		if got.Idle != newer.Idle {
			t.Fatal("earlier run boundary overwrote newer explicit settings")
		}
	})
}
