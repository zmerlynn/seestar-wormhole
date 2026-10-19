package cloud

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// The oracle is independent of the join implementation and error traversal.
func outcomeFixtures() []struct {
	err     error
	rank    Severity
	blocked bool
} {
	return []struct {
		err     error
		rank    Severity
		blocked bool
	}{
		{nil, Success, false},
		{ErrTransport, Retryable, false},
		{ErrOffline, Retryable, false},
		{context.DeadlineExceeded, Retryable, false},
		{ErrOwedRelease, OwedRelease, false},
		{ErrInvalid, Malformed, false},
		{ErrLicenseMissing, Malformed, false},
		{ErrBusy, Busy, false},
		{ErrUnauthorized, Denied, false},
		{ErrRejected, Denied, false},
		{ErrInternal, Fatal, false},
		{ReleaseOutcome(ErrTransport), OwedRelease, false},
		{ReleaseOutcome(ErrInvalid), Malformed, true},
		{ReleaseOutcome(ErrUnauthorized), Denied, true},
		{errors.New("internal test failure"), Fatal, false},
		{context.Canceled, Retryable, false},
		{ErrAccountUncertain, Malformed, false},
	}
}

func checkOutcomeOrder(t *testing.T, choices []byte) {
	t.Helper()
	fixtures := outcomeFixtures()
	if len(choices) > 32 {
		choices = choices[:32]
	}
	var observed []error
	want, blocked, owed := Success, false, false
	var accumulated error
	for _, choice := range choices {
		fixture := fixtures[int(choice)%len(fixtures)]
		observed = append(observed, fixture.err)
		if fixture.rank > want {
			want = fixture.rank
		}
		blocked = blocked || fixture.blocked
		owed = owed || errors.Is(fixture.err, ErrOwedRelease)
		accumulated = JoinOutcomes(accumulated, fixture.err)
		if got := Disposition(accumulated); got != want {
			t.Fatalf("prefix %v disposition=%d, want maximum %d", choices, got, want)
		}
	}
	for permutation := 0; permutation <= len(observed); permutation++ {
		joined := JoinOutcomes(observed...)
		if got := Disposition(joined); got != want {
			t.Fatalf("permutation %v disposition=%d, want maximum %d", choices, got, want)
		}
		if joined != nil && (joined.(*Outcome).ReleaseBlocked() != blocked || errors.Is(joined, ErrOwedRelease) != owed) {
			t.Fatalf("permutation loses release metadata: %v", choices)
		}
		// Duplication and arbitrary grouping must preserve disposition and metadata.
		repeated := JoinOutcomes(joined, joined, accumulated)
		if Disposition(repeated) != want {
			t.Fatalf("duplicate/grouping lowers %v", choices)
		}
		if len(observed) > 0 {
			observed = append(observed[1:], observed[0])
		}
	}
	// Reverse order exercises all ordered pairs in the exhaustive table below.
	for i, j := 0, len(observed)-1; i < j; i, j = i+1, j-1 {
		observed[i], observed[j] = observed[j], observed[i]
	}
	if Disposition(JoinOutcomes(observed...)) != want {
		t.Fatalf("reversed join lowers %v", choices)
	}
}

func TestOutcomeTotalOrder(t *testing.T) {
	order := []Severity{Success, Retryable, OwedRelease, Malformed, Busy, Denied, Fatal}
	for i := 1; i < len(order); i++ {
		if order[i] <= order[i-1] {
			t.Fatalf("severity order %v is not total", order)
		}
	}
	// Exhaust all combinations of three outcomes, plus rotations/reversal,
	// prefix monotonicity, duplicate observations and grouped joins.
	for a := range outcomeFixtures() {
		for b := range outcomeFixtures() {
			for c := range outcomeFixtures() {
				checkOutcomeOrder(t, []byte{byte(a), byte(b), byte(c)})
			}
		}
	}
}

func TestOutcomePreservesTypedDiagnostics(t *testing.T) {
	_, rejected := ParseLogin(401, nil, false)
	joined := JoinOutcomes(fmt.Errorf("fixed operation: %w", rejected), ErrTransport)
	var account *AccountError
	if !errors.As(joined, &account) || account.Status != 401 || Disposition(joined) != Denied {
		t.Fatalf("typed denial=%v, want HTTP 401 denied", joined)
	}
	_, absent := ParseLogin(0, nil, false)
	if got := Disposition(JoinOutcomes(absent, ErrTransport)); got != Retryable {
		t.Fatalf("absent response disposition=%d, want retryable", got)
	}
}

func FuzzOutcomeJoin(f *testing.F) {
	for _, seed := range [][]byte{{}, {1, 5}, {5, 1}, {1, 12, 3}, {11, 2, 7, 9}, {0, 4, 6, 8, 10, 13}, {15, 14, 7, 9}, {7, 14, 9, 15}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, choices []byte) { checkOutcomeOrder(t, choices) })
}
