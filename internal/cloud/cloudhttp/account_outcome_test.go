package cloudhttp

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestAccountCancellationReplyPermutations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, failure := range []error{nil, cloud.ErrInvalid, cloud.ErrRejected, cloud.ErrInternal} {
			for _, ready := range []bool{false, true} {
				for _, cancelBeforeReply := range []bool{false, true} {
					o := &accountOwner{jobs: make(chan accountJob), done: make(chan struct{})}
					ctx, cancel := context.WithCancel(t.Context())
					reply := make(chan accountReply, 1)
					result := make(chan accountReply, 1)
					go func() {
						j := <-o.jobs
						if cancelBeforeReply {
							cancel()
						}
						if ready {
							j.reply.publish(accountReply{err: failure})
						}
						reply <- accountReply{}
						if !cancelBeforeReply {
							cancel()
						}
					}()
					go func() { result <- o.call(ctx, cloud.AccountEvent{Kind: cloud.AccountBegin}) }()
					<-reply
					synctest.Wait()
					r := <-result
					// If a reply won before cancellation it remains authoritative. Otherwise
					// unresolved completion must block instead of returning bare transport.
					if !ready && cloud.Disposition(r.err) < cloud.Malformed {
						t.Errorf("ready=%v cancel-first=%v err=%v rank=%d want uncertain", ready, cancelBeforeReply, r.err, cloud.Disposition(r.err))
					}
					if ready && failure != nil && cloud.Disposition(r.err) < cloud.Malformed {
						t.Errorf("ready=%v cancel-first=%v failure=%v result=%v loses known outcome", ready, cancelBeforeReply, failure, r.err)
					}
					if !ready && !errors.Is(r.err, context.Canceled) {
						t.Error("lost cancellation cause")
					}
					cancel()
				}
			}
		}
	})
}

func TestAccountCancellationUnknownIsPrompt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &accountOwner{jobs: make(chan accountJob), done: make(chan struct{})}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		started := make(chan struct{})
		go func() { <-o.jobs; close(started) }()
		result := make(chan accountReply, 1)
		go func() { result <- o.call(ctx, cloud.AccountEvent{Kind: cloud.AccountBegin}) }()
		<-started
		before := time.Now()
		r := <-result
		if time.Since(before) != time.Second || !errors.Is(r.err, context.DeadlineExceeded) || cloud.Disposition(r.err) < cloud.Malformed {
			t.Fatalf("unknown reply elapsed=%v err=%v rank=%d want prompt deadline and uncertainty", time.Since(before), r.err, cloud.Disposition(r.err))
		}
	})
}

func TestCanceledAccountReplyRetainsPublishedOutcome(t *testing.T) {
	for _, failure := range []error{nil, cloud.ErrInvalid, cloud.ErrRejected, cloud.ErrInternal} {
		for _, interrupted := range []error{context.Canceled, context.DeadlineExceeded} {
			reply := newAccountCompletion()
			reply.publish(accountReply{err: failure})
			r := canceledAccountReply(reply, interrupted)
			if cloud.Disposition(r.err) != max(cloud.Retryable, cloud.Disposition(failure)) || !errors.Is(r.err, interrupted) || failure != nil && !errors.Is(r.err, failure) {
				t.Fatalf("published=%v interruption=%v result=%v loses classification/cause", failure, interrupted, r.err)
			}
		}
	}
}
