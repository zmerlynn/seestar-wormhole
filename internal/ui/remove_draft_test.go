package ui

import (
	"encoding/json/v2"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"testing"
)

func TestRemoveDoesNotSaveUnstartedDraft(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid-unstarted-edit", true: "invalid-ports"}[invalid], func(t *testing.T) {
			s := testServer(t)
			working := testSettings()
			working.Remember = true
			s.settings = working
			s.controller.credentials.savedEmail = working.Email
			s.controller.credentials.chain.(*fakeKeychain).value = testPassword
			if err := s.storage.write("config.json", working); err != nil {
				t.Fatal(err)
			}
			draft := working
			draft.Idle = "17m"
			if invalid {
				draft.Ports = []proxyrun.Port{{Device: 999}}
			}
			b, _ := json.Marshal(input{Settings: draft})
			if w := doPost(s, "/api/forget", string(b)); w.Code != 200 {
				t.Fatal("removal did not succeed")
			}
			got, err := loadSettings(s.storage)
			if err != nil {
				t.Fatal("successful Remove persisted invalid draft and made config unloadable")
			}
			if got.Remember || got.Idle != working.Idle {
				t.Fatal("Remove saved unrelated unstarted draft instead of only removing association")
			}
		})
	}
}
