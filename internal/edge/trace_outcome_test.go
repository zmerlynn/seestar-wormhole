package edge

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
)

func TestDTLSScalarOutcome(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, "none"}, {context.Canceled, "canceled"}, {context.DeadlineExceeded, "timeout"},
		{os.ErrDeadlineExceeded, "timeout"}, {net.ErrClosed, "operation_failed"},
		{errors.New("private-password-token-path"), "operation_failed"},
	} {
		if got := dtlsOutcome(tc.err); got != tc.want {
			t.Fatalf("outcome=%q want %q", got, tc.want)
		}
	}
}
