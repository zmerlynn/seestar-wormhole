package main

import (
	"context"
	"errors"
	smb2 "github.com/cloudsoda/go-smb2"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"io"
	"net"
	"testing"
)

func TestCompletedSMBShutdownPreservesFailures(t *testing.T) {
	closed := &smb2.TransportError{Err: io.EOF}
	for _, test := range []struct {
		name      string
		err, want error
	}{
		{"clean-transport", closed, nil},
		{"closed-direct-session", &smb2.TransportError{Err: session.ErrClosed}, nil},
		{"arbitrary-session-close", session.ErrClosed, session.ErrClosed},
		{"arbitrary-shutdown-eof", io.EOF, io.EOF},
		{"arbitrary-shutdown-close", net.ErrClosed, net.ErrClosed},
		{"cancellation", &smb2.TransportError{Err: context.Canceled}, context.Canceled},
		{"deadline", &smb2.TransportError{Err: context.DeadlineExceeded}, context.DeadlineExceeded},
		{"release-sibling", errors.Join(closed, session.ErrStop), session.ErrStop},
		{"disk-sibling", errors.Join(closed, &diagnostic.PullError{Reason: diagnostic.DiskFull}), &diagnostic.PullError{Reason: diagnostic.DiskFull}},
		{"joined-transport-sibling", &smb2.TransportError{Err: errors.Join(io.EOF, errors.ErrUnsupported)}, errors.ErrUnsupported},
		{"policy-inside-transport", &smb2.TransportError{Err: &diagnostic.PullError{Reason: diagnostic.VerifyMismatch, Cause: io.EOF}}, &diagnostic.PullError{Reason: diagnostic.VerifyMismatch}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := completedSMBShutdownError(test.err)
			if test.want == nil {
				if got != nil {
					t.Fatalf("close=%v", got)
				}
				return
			}
			if want, ok := test.want.(*diagnostic.PullError); ok {
				var detail *diagnostic.PullError
				if !errors.As(got, &detail) || detail.Reason != want.Reason {
					t.Fatalf("lost policy: %v", got)
				}
			} else if !errors.Is(got, test.want) {
				t.Fatalf("got=%v want=%v", got, test.want)
			}
		})
	}
}
func TestCompletedSMBDeadlineKeepsJoinedFailure(t *testing.T) {
	raw, peer := net.Pipe()
	defer peer.Close()
	p := &pullConnections{connections: map[*pullConn]struct{}{}}
	c := &pullConn{Conn: deadlineFailureConn{raw, errors.Join(net.ErrClosed, errors.ErrUnsupported)}, owner: p}
	p.connections[c] = struct{}{}
	if err := p.shutdownAfterWork(shutdownFailureFs{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("lost deadline sibling: %v", err)
	}
}

// A direct Session closes its streams with ErrClosed before the pull watcher
// necessarily runs. This exact owner sentinel must preserve cancellation;
// opaque policies and independent joined siblings must remain failures.
func TestPullDirectSessionClosureRetainsWorkCause(t *testing.T) {
	for _, done := range []bool{false, true} {
		for _, cause := range []error{session.ErrClosed, errors.Join(session.ErrClosed, errors.ErrUnsupported), &diagnostic.PullError{Reason: diagnostic.DiskFull, Cause: session.ErrClosed}} {
			ctx, cancel := context.WithCancel(t.Context())
			if done {
				cancel()
			}
			c := &pullConn{workContext: ctx}
			got := c.workError(cause)
			want := cause
			if done && cause == session.ErrClosed {
				want = context.Canceled
			}
			if !errors.Is(got, want) {
				t.Fatalf("done=%t got=%v want=%v", done, got, want)
			}
			cancel()
		}
	}
}
