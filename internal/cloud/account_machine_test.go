package cloud

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"testing"
	"time"
)

func TestAccountStateMachine(t *testing.T) {
	creds, _ := EmailCredentials("reader@example.invalid", "Example-Only-123")
	m := NewAccountMachine(creds)
	a := m.Step(AccountEvent{Kind: AccountBegin}, 0)
	if a.Kind != AccountSendLogin || a.Renewal {
		t.Fatal("initial login")
	}
	// Stale completions cannot install credentials.
	if got := m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence + 1, Token: AccessToken{"stale"}}, 1); got.Kind != AccountNone {
		t.Fatal("stale completion")
	}
	a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Token: AccessToken{"first"}}, 2)
	if a.Kind != AccountSendProfile || m.Token().Value() != "first" {
		t.Fatal("install before profile")
	}
	for i := 0; i < 2; i++ {
		a = m.Step(AccountEvent{Kind: AccountProfileDone, Sequence: a.Sequence, Err: ErrUnauthorized}, 3)
		if a.Kind != AccountSendLogin || !a.Renewal {
			t.Fatal("401 must renew")
		}
		a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Token: AccessToken{"next"}}, 4)
		if a.Kind != AccountSendProfile {
			t.Fatal("replay profile")
		}
	}
	a = m.Step(AccountEvent{Kind: AccountProfileDone, Sequence: a.Sequence, Err: ErrUnauthorized}, 5)
	if a.Kind != AccountComplete || !errors.Is(a.Err, ErrReauthentication) || m.Token().Value() != "" {
		t.Fatal("third 401 must stop and clear")
	}
	a = m.Step(AccountEvent{Kind: AccountBegin}, 6)
	if a.Kind != AccountComplete || !errors.Is(a.Err, ErrReauthentication) {
		t.Fatal("no blind re-login")
	}
}

func TestAccountNoBlindRetryAndLogout(t *testing.T) {
	for _, failure := range []error{ErrRejected, ErrInvalid} {
		creds, _ := EmailCredentials("reader@example.invalid", "Example-Only-123")
		m := NewAccountMachine(creds)
		a := m.Step(AccountEvent{Kind: AccountBegin}, 0)
		a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Err: failure}, 0)
		if a.Kind != AccountComplete || !errors.Is(a.Err, failure) {
			t.Fatal("login failure lost")
		}
		if a = m.Step(AccountEvent{Kind: AccountBegin}, 0); a.Kind != AccountComplete || !errors.Is(a.Err, failure) {
			t.Fatal("failure retried")
		}
	}
	creds, _ := EmailCredentials("reader@example.invalid", "Example-Only-123")
	m := NewAccountMachine(creds)
	a := m.Step(AccountEvent{Kind: AccountBegin}, 0)
	seq := a.Sequence
	m.Step(AccountEvent{Kind: AccountLogout}, 0)
	m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: seq, Token: AccessToken{"late"}}, 0)
	if m.Token().Value() != "" || m.credentials.password != "" {
		t.Fatal("late token resurrected logout")
	}
}

func TestOldRemoteExpiryCannotCancelCurrentProfile(t *testing.T) {
	creds, _ := EmailCredentials("reader@example.invalid", "Example-Only-123")
	m := NewAccountMachine(creds)
	a := m.Step(AccountEvent{Kind: AccountBegin}, 0)
	a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Token: AccessToken{"new"}}, 0)
	seq := a.Sequence
	m.Step(AccountEvent{Kind: AccountRemoteExpired, Token: AccessToken{"old"}}, 1)
	a = m.Step(AccountEvent{Kind: AccountProfileDone, Sequence: seq}, 2)
	if a.Kind != AccountComplete || a.Err != nil || a.Token.Value() != "new" {
		t.Fatal("stale remote expiry canceled profile operation")
	}
}

func TestAccountAcceleratedNight(t *testing.T) {
	creds, _ := EmailCredentials("reader@example.invalid", "Example-Only-123")
	m := NewAccountMachine(creds)
	a := m.Step(AccountEvent{Kind: AccountBegin}, 0)
	for minute := 0; minute < 8*60; minute++ {
		now := core.Instant(minute) * core.Instant(time.Minute)
		if a.Kind == AccountSendLogin {
			a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Token: AccessToken{"opaque"}}, now)
		}
		if a.Kind != AccountSendProfile {
			t.Fatal("profile request missing")
		}
		if minute > 0 && minute%60 == 0 {
			a = m.Step(AccountEvent{Kind: AccountProfileDone, Sequence: a.Sequence, Err: ErrUnauthorized}, now)
			if a.Kind != AccountSendLogin || !a.Renewal {
				t.Fatal("hourly observed expiry")
			}
			a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Token: AccessToken{"opaque"}}, now)
		}
		seq := a.Sequence
		// Duplication/reordering and forward/backward clock jumps have no effect on
		// expiry: only the matching HTTP completion may transition this operation.
		m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: seq - 1, Token: AccessToken{"stale"}}, now+core.Instant(24*time.Hour))
		m.Step(AccountEvent{Kind: AccountProfileDone, Sequence: seq + 1, Err: ErrUnauthorized}, 0)
		a = m.Step(AccountEvent{Kind: AccountProfileDone, Sequence: seq}, now)
		if a.Kind != AccountComplete || a.Err != nil {
			t.Fatal("night operation failed")
		}
		a = m.Step(AccountEvent{Kind: AccountCheckProfile}, now)
	}
	m.Step(AccountEvent{Kind: AccountLogout}, 0)
	if m.Token().Value() != "" || m.credentials.password != "" {
		t.Fatal("night cleanup")
	}
}
