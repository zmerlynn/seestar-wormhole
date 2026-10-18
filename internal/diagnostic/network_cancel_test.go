package diagnostic

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"net"
	"testing"
)

func TestDialContextCancellationLeafAndSiblings(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("dial error=%v", err)
	}
	for _, test := range []struct {
		err  error
		want string
	}{
		{err, "canceled"}, {errors.Join(err, context.Canceled), "canceled"},
		{errors.Join(err, errors.New("disk")), "operation_failed"},
		{errors.Join(err, session.ErrStop), "release_failed"},
		{errors.Join(err, context.DeadlineExceeded), "timeout"},
	} {
		if _, got := Fields(test.err, "pull"); got != test.want {
			t.Errorf("%v: reason=%s want %s", test.err, got, test.want)
		}
	}
}
