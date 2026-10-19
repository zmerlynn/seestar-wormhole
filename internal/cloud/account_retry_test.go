package cloud

import (
	"errors"
	"testing"
)

func TestAccountTransportFailureAllowsFreshLogin(t *testing.T) {
	creds, _ := EmailCredentials("fixture@example.invalid", "fixture-password")
	m := NewAccountMachine(creds)
	a := m.Step(AccountEvent{Kind: AccountBegin}, 0)
	seq := a.Sequence
	a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: seq, Err: ErrTransport}, 1)
	if !errors.Is(a.Err, ErrTransport) {
		t.Fatalf("failure=%v", a.Err)
	}
	a = m.Step(AccountEvent{Kind: AccountBegin}, 2)
	if a.Kind != AccountSendLogin || a.Sequence <= seq || a.Credentials != creds {
		t.Fatalf("retry=%v, want fresh login with retained credentials", a.Kind)
	}
	if stale := m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: seq, Token: AccessToken{"stale"}}, 3); stale.Kind != AccountNone {
		t.Fatal("retired completion accepted")
	}
	a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Token: AccessToken{"fresh"}}, 4)
	if a.Kind != AccountSendProfile {
		t.Fatalf("login progress=%v", a.Kind)
	}
	m.Step(AccountEvent{Kind: AccountLogout}, 5)
	if m.credentials != (Credentials{}) || m.Token().Value() != "" {
		t.Fatal("logout retained retry credentials")
	}
}
