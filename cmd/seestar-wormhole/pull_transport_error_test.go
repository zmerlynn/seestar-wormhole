package main

import (
	"context"
	"errors"
	smb2 "github.com/cloudsoda/go-smb2"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"testing"
)

func TestReviewSMBTransportCancellationProjection(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{"cancel-only", &smb2.TransportError{Err: context.Canceled}, 130},
		{"cancel-joined", errors.Join(context.Canceled, &smb2.TransportError{Err: context.Canceled}), 130},
		{"release-sibling", errors.Join(&smb2.TransportError{Err: context.Canceled}, session.ErrStop), 1},
		{"disk-sibling", errors.Join(&smb2.TransportError{Err: context.Canceled}, errors.ErrUnsupported), 1},
		{"timeout", &smb2.TransportError{Err: context.DeadlineExceeded}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			phase, reason := diagnostic.Fields(test.err, "pull")
			code := pullCode(test.err)
			if code != test.want {
				t.Fatalf("exit=%d want=%d phase=%s reason=%s cause=%T %v", code, test.want, phase, reason, test.err, test.err)
			}
		})
	}
}
