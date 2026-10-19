package cloud

import (
	"context"
	"errors"
	"testing"
)

// Extend the outcome permutation property through its lifecycle consumers:
// each replay round, login/profile completion, cancellation, then terminal and
// stale events. The expected rank comes from the independent failure fixture.
func TestOutcomeAccountLifecyclePermutations(t *testing.T) {
	for _, failure := range []error{ErrInvalid, ErrRejected, ErrInternal} {
		for _, interrupted := range []error{context.Canceled, context.DeadlineExceeded} {
			causes := []error{failure, interrupted, ErrTransport}
			for i := range causes {
				for j := range causes {
					if i == j {
						continue
					}
					k := 3 - i - j
					joined := JoinOutcomes(JoinOutcomes(causes[i], causes[j]), causes[k])
					for rounds := 0; rounds <= 2; rounds++ {
						for _, profile := range []bool{false, true} {
							checkAccountLifecycleOutcome(t, joined, failure, rounds, profile)
						}
					}
				}
			}
		}
	}
	// Fatal must dominate the challenge itself in every cause order, as well as
	// surviving a fatal completion during a later login/profile replay.
	for _, interrupted := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, order := range [][]error{{ErrInternal, ErrUnauthorized, interrupted}, {ErrInternal, interrupted, ErrUnauthorized}, {ErrUnauthorized, ErrInternal, interrupted}, {ErrUnauthorized, interrupted, ErrInternal}, {interrupted, ErrInternal, ErrUnauthorized}, {interrupted, ErrUnauthorized, ErrInternal}} {
			for rounds := 0; rounds <= 2; rounds++ {
				checkAccountLifecycleOutcome(t, JoinOutcomes(order...), ErrInternal, rounds, true)
			}
		}
	}
}

func checkAccountLifecycleOutcome(t *testing.T, joined, failure error, rounds int, profile bool) {
	t.Helper()
	creds, _ := EmailCredentials("fixture@example.invalid", "fixture-password")
	m := NewAccountMachine(creds)
	a := m.Step(AccountEvent{Kind: AccountBegin}, 0)
	for i := 0; i < rounds; i++ {
		a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Token: AccessToken{"first"}}, 0)
		a = m.Step(AccountEvent{Kind: AccountProfileDone, Sequence: a.Sequence, Err: ErrUnauthorized}, 0)
		if a.Kind != AccountSendLogin || !a.Renewal {
			t.Fatal("ordinary challenge did not renew")
		}
	}
	kind := AccountLoginDone
	if profile {
		a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Token: AccessToken{"renewed"}}, 0)
		kind = AccountProfileDone
	}
	a = m.Step(AccountEvent{Kind: kind, Sequence: a.Sequence, Err: joined}, 0)
	want := Disposition(failure)
	if a.Kind != AccountComplete || Disposition(a.Err) != want {
		t.Fatalf("rounds/profile=%d/%v got kind/rank=%d/%d want complete/%d", rounds, profile, a.Kind, Disposition(a.Err), want)
	}
	for _, event := range []AccountEvent{{Kind: AccountBegin}, {Kind: AccountCheckProfile}, {Kind: AccountLogout}, {Kind: AccountRemoteExpired}, {Kind: kind, Sequence: 1, Token: AccessToken{"stale"}}} {
		a = m.Step(event, 0)
		if a.Kind != AccountComplete || Disposition(a.Err) < want || !errors.Is(a.Err, failure) {
			t.Fatalf("event=%d rounds/profile=%d/%v loses blocking outcome: kind=%d err=%v", event.Kind, rounds, profile, a.Kind, a.Err)
		}
	}
}

func TestProfileChallengeKeepsIndependentBlockingCauses(t *testing.T) {
	for _, failure := range []error{ErrInvalid, ErrRejected, ErrAccountUncertain, ErrBusy, ErrOwedRelease} {
		for _, order := range [][]error{{failure, ErrUnauthorized, context.Canceled}, {ErrUnauthorized, context.Canceled, failure}, {context.Canceled, failure, ErrUnauthorized}} {
			creds, _ := EmailCredentials("fixture@example.invalid", "fixture-password")
			m := NewAccountMachine(creds)
			a := m.Step(AccountEvent{Kind: AccountBegin}, 0)
			a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Token: AccessToken{"first"}}, 0)
			a = m.Step(AccountEvent{Kind: AccountProfileDone, Sequence: a.Sequence, Err: JoinOutcomes(order...)}, 0)
			if a.Kind != AccountComplete || Disposition(a.Err) != Denied || !errors.Is(a.Err, failure) {
				t.Fatalf("401 plus %v: kind=%d error=%v want retained denial", failure, a.Kind, a.Err)
			}
		}
	}
}

func TestReplayRejectionDoesNotEraseIndependentFatal(t *testing.T) {
	for _, status := range []int{401, 403} {
		creds, _ := EmailCredentials("fixture@example.invalid", "fixture-password")
		m := NewAccountMachine(creds)
		a := m.Step(AccountEvent{Kind: AccountBegin}, 0)
		token, _ := ParseLogin(200, []byte(`{"data":{"accessToken":"opaque"}}`), false)
		a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Token: token}, 0)
		a = m.Step(AccountEvent{Kind: AccountProfileDone, Sequence: a.Sequence, Err: ErrUnauthorized}, 0)
		_, rejection := ParseLogin(status, []byte(`{"code":9876,"data":null}`), true)
		a = m.Step(AccountEvent{Kind: AccountLoginDone, Sequence: a.Sequence, Err: JoinOutcomes(rejection, ErrInternal)}, 0)
		for _, event := range []AccountEvent{{Kind: AccountBegin}, {Kind: AccountCheckProfile}, {Kind: AccountLogout}} {
			a = m.Step(event, 0)
			var statusError *AccountError
			if !errors.Is(a.Err, ErrReauthentication) || errors.As(a.Err, &statusError) || errors.Is(a.Err, ErrRejected) {
				t.Error("later fatal operation inherited replay status instead of requiring reauthentication")
			}
			if a.Kind != AccountComplete || Disposition(a.Err) != Fatal || !errors.Is(a.Err, ErrInternal) || m.credentials != (Credentials{}) || m.Token().Value() != "" {
				t.Fatal("replay rejection erased fatal failure or restored credentials")
			}
		}
	}
}
