package main

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestReplayReauthenticationCommandProjection(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			creds, _ := cloud.EmailCredentials("reader@example.invalid", "Review-Only-123")
			m := cloud.NewAccountMachine(creds)
			a := m.Step(cloud.AccountEvent{Kind: cloud.AccountBegin}, 0)
			token, _ := cloud.ParseLogin(200, []byte(`{"data":{"accessToken":"token-secret"}}`), false)
			a = m.Step(cloud.AccountEvent{Kind: cloud.AccountLoginDone, Sequence: a.Sequence, Token: token}, 0)
			a = m.Step(cloud.AccountEvent{Kind: cloud.AccountProfileDone, Sequence: a.Sequence}, 0)
			a = m.Step(cloud.AccountEvent{Kind: cloud.AccountCheckProfile}, 0)
			a = m.Step(cloud.AccountEvent{Kind: cloud.AccountProfileDone, Sequence: a.Sequence, Err: cloud.ErrUnauthorized}, 0)
			_, rejection := cloud.ParseLogin(status, []byte(`{"code":9876,"msg":"identity-secret","data":null}`), true)
			a = m.Step(cloud.AccountEvent{Kind: cloud.AccountLoginDone, Sequence: a.Sequence, Err: rejection}, 0)
			if !errors.Is(a.Err, cloud.ErrRejected) {
				t.Fatal("original rejection lost")
			}
			a = m.Step(cloud.AccountEvent{Kind: cloud.AccountBegin}, 0)
			// The cloud edge projects denied disposition to uncertain session ownership.
			err := diagnostic.AtLayer(a.Err, session.Cloud, session.ErrUncertain)
			for _, command := range []commandKind{commandProxy} {
				if code := commandExitCode(options{command: command}, err); code != 1 {
					t.Errorf("command=%v exit=%d, want 1", command, code)
				}
			}
			var logs bytes.Buffer
			diagnostic.LogFinal(slog.New(slog.NewJSONHandler(&logs, nil)), "command_failed", "cloud", err, nil)
			if !strings.Contains(logs.String(), `"reason":"reauthentication_required"`) {
				t.Errorf("later command reason=%s", &logs)
			}
			for _, secret := range []string{"reader@example.invalid", "Review-Only-123", "token-secret", "identity-secret", "9876"} {
				if strings.Contains(logs.String()+fmt.Sprintf("%v %+v %#v", err, err, err), secret) {
					t.Error("secret reached command diagnostics")
				}
			}
		})
	}
}
