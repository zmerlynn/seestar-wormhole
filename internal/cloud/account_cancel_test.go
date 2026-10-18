package cloud

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestAccountInitialLoginCancellation(t *testing.T) {
	for _, cancellation := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, rounds := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("%s/replays=%d", cancellation, rounds), func(t *testing.T) {
				creds, _ := EmailCredentials("reader@example.invalid", "example-password")
				m := NewAccountMachine(creds)
				a := m.Step(AccountEvent{Kind: AccountBegin}, 0)
				for i := 0; i < rounds; i++ {
					a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Token: AccessToken{"challenged"}}, 0)
					a = m.Step(AccountEvent{Kind: AccountProfileDone, Sequence: a.Sequence, Err: ErrUnauthorized}, 0)
				}
				old := a.Sequence
				a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: old, Err: cancellation}, 0)
				if a.Kind != AccountComplete || !errors.Is(a.Err, cancellation) {
					t.Fatalf("cancellation = %+v", a)
				}
				if m.Token().Value() != "" || a.Token.Value() != "" {
					t.Fatal("canceled replay retained challenged token")
				}
				a = m.Step(AccountEvent{Kind: AccountBegin}, 0)
				if a.Kind != AccountSendLogin || a.Sequence <= old || a.Credentials != creds || a.Renewal {
					t.Fatal("canceled initial login cannot restart")
				}
				if a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: old, Token: AccessToken{"stale"}}, 0); a.Kind != AccountNone || m.Token().Value() != "" {
					t.Fatal("stale canceled completion accepted")
				}
				m.Step(AccountEvent{Kind: AccountLogout}, 0)
				if a = m.Step(AccountEvent{Kind: AccountBegin}, 0); !errors.Is(a.Err, ErrReauthentication) || m.credentials != (Credentials{}) {
					t.Fatal("logout resurrected credentials")
				}
			})
		}
	}
}

// A rejection with a received status wins over an interrupted body read. A
// status-zero ParseLogin error represents no received reply and must still resume.
func TestAccountReceivedRejectionPrecedesCancellation(t *testing.T) {
	for _, cancellation := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, rounds := range []int{0, 1, 2} {
			for _, status := range []int{0, 401, 403, 500} {
				t.Run(fmt.Sprintf("%s/replays=%d/status=%d", cancellation, rounds, status), func(t *testing.T) {
					creds, _ := EmailCredentials("reader@example.invalid", "example-password")
					m := NewAccountMachine(creds)
					a := m.Step(AccountEvent{Kind: AccountBegin}, 0)
					for i := 0; i < rounds; i++ {
						a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Token: AccessToken{"challenged"}}, 0)
						a = m.Step(AccountEvent{Kind: AccountProfileDone, Sequence: a.Sequence, Err: ErrUnauthorized}, 0)
					}
					_, rejection := ParseLogin(status, nil, rounds > 0)
					m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Err: errors.Join(cancellation, rejection)}, 0)
					a = m.Step(AccountEvent{Kind: AccountBegin}, 0)
					if status == 0 {
						if a.Kind != AccountSendLogin {
							t.Fatal("response-less interruption became terminal")
						}
					} else {
						want := ErrRejected
						if rounds > 0 && (status == 401 || status == 403) {
							want = ErrReauthentication
						}
						if a.Kind != AccountComplete || !errors.Is(a.Err, want) || m.credentials != (Credentials{}) || m.Token().Value() != "" {
							t.Fatalf("received rejection was resumable: status=%d action=%v", status, a.Kind)
						}
					}
				})
			}
		}
	}
}
