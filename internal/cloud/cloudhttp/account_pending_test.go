package cloudhttp

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestPendingAccountClassification(t *testing.T) {
	for _, eventual := range []error{nil, cloud.ErrTransport, cloud.ErrInvalid, cloud.ErrRejected, cloud.ErrInternal} {
		for _, known := range []error{nil, cloud.ErrInvalid, cloud.ErrRejected, cloud.ErrInternal} {
			synctest.Test(t, func(t *testing.T) {
				c := newAccountCompletion()
				err := cloud.JoinOutcomes(context.DeadlineExceeded, &pendingAccount{c}, known)
				if cloud.Disposition(err) < cloud.Malformed {
					t.Fatal("unresolved completion allowed retry")
				}
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				unresolved := ResolveAccount(ctx, err)
				if !errors.Is(unresolved, cloud.ErrAccountUncertain) || cloud.Disposition(unresolved) < cloud.Malformed {
					t.Fatal("cancellation lost pending ownership")
				}
				go func() { <-time.After(20 * time.Millisecond); c.publish(accountReply{err: eventual}) }()
				started := time.Now()
				result := ResolveAccount(t.Context(), err)
				want := max(cloud.Retryable, cloud.Disposition(known), cloud.Disposition(eventual))
				if time.Since(started) != 20*time.Millisecond || cloud.Disposition(result) != want || errors.Is(result, cloud.ErrAccountUncertain) || !errors.Is(result, context.DeadlineExceeded) {
					t.Fatalf("eventual=%v known=%v result=%v rank=%d want %d after joined completion", eventual, known, result, cloud.Disposition(result), want)
				}
				if known != nil && !errors.Is(result, known) || eventual != nil && !errors.Is(result, eventual) {
					t.Fatal("lost known cause")
				}
				// Publication is reusable by concurrent or later consumers.
				if again := ResolveAccount(t.Context(), err); cloud.Disposition(again) != want {
					t.Fatal("second waiter lost completion")
				}
			})
		}
	}
}

func TestPendingAccountPreservesUnknownAndRelease(t *testing.T) {
	c := newAccountCompletion()
	c.publish(accountReply{})
	unknown := errors.New("unknown internal outcome")
	for _, known := range []error{unknown, cloud.ReleaseOutcome(cloud.ErrRejected), cloud.ErrAccountUncertain} {
		result := ResolveAccount(t.Context(), cloud.JoinOutcomes(&pendingAccount{c}, known))
		if cloud.Disposition(result) != cloud.Disposition(known) {
			t.Fatalf("resolution lowered/lost known cause: %v", result)
		}
		if outcome, ok := known.(*cloud.Outcome); ok && outcome.ReleaseBlocked() && !result.(*cloud.Outcome).ReleaseBlocked() {
			t.Fatal("lost blocking release metadata")
		}
	}
}

func TestPendingAccountConcurrentWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := newAccountCompletion()
		pending := &pendingAccount{c}
		// errors.Join exposes its underlying slice through Unwrap; resolving must
		// never change the original tree shared by these consumers.
		err := errors.Join(context.DeadlineExceeded, pending, cloud.ErrInvalid)
		replies := make(chan error, 8)
		for range 8 {
			go func() { replies <- ResolveAccount(t.Context(), err) }()
		}
		synctest.Wait()
		c.publish(accountReply{err: cloud.ErrRejected})
		for range 8 {
			result := <-replies
			if cloud.Disposition(result) != cloud.Denied || !errors.Is(result, cloud.ErrInvalid) || !errors.Is(result, cloud.ErrRejected) {
				t.Fatalf("lost concurrent classification: %v", result)
			}
		}
		if !errors.Is(err, cloud.ErrAccountUncertain) {
			t.Fatal("resolution mutated original observation")
		}
	})
}

func TestAccountClosePublishesEveryAdmittedCompletion(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "synthetic-password")
	synctest.Test(t, func(t *testing.T) {
		admitted := 0
		for _, terminal := range []bool{false, true} {
			for range 100 {
				c, err := NewFromEnvironment(Config{StateDir: t.TempDir(), Transport: closedAccountTransport{}})
				if err != nil {
					t.Fatal(err)
				}
				if terminal {
					if err = c.Login(t.Context()); !errors.Is(err, cloud.ErrInternal) {
						t.Fatalf("initial fatal observation=%v", err)
					}
				}
				job := accountJob{ctx: t.Context(), event: cloud.AccountEvent{Kind: cloud.AccountBegin}, reply: newAccountCompletion()}
				c.account.cancel()
				select {
				case c.account.jobs <- job:
					admitted++
				case <-c.account.done:
					c.Close()
					continue
				}
				c.Close()
				select {
				case <-job.reply.done:
					if terminal && !errors.Is(job.reply.result.err, cloud.ErrInternal) {
						t.Error("shutdown publication lowered the retained fatal observation")
					}
					if !errors.Is(job.reply.result.err, cloud.ErrReauthentication) {
						t.Errorf("closed owner classification=%v want reauthentication", job.reply.result.err)
					}
				default:
					t.Error("account owner exited without publishing an admitted completion")
				}
			}
		}
		if admitted == 0 {
			t.Fatal("no admitted shutdown schedule exercised")
		}
	})
}

type closedAccountTransport struct{}

func (closedAccountTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("synthetic account failure")
}

func TestPendingAccountJoinsBlockingReleaseClassification(t *testing.T) {
	c := newAccountCompletion()
	c.publish(accountReply{err: cloud.ErrInternal})
	known := cloud.ReleaseOutcome(cloud.ErrRejected)
	err := cloud.JoinOutcomes(known, &pendingAccount{c}, context.DeadlineExceeded)
	result := ResolveAccount(t.Context(), err)
	if cloud.Disposition(result) != cloud.Fatal || !errors.Is(result, cloud.ErrInternal) || !errors.Is(result, cloud.ErrRejected) || errors.Is(result, cloud.ErrAccountUncertain) || !result.(*cloud.Outcome).ReleaseBlocked() {
		t.Fatalf("joined blocking release/account result=%v rank=%d want retained fatal/denied/release metadata", result, cloud.Disposition(result))
	}
}
