package diagnostic

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"io"
	"net"
	"strings"
	"testing"
)

type backendCause struct{}

func (backendCause) Error() string { return "BACKEND_TEXT" }
func TestCauseDiagnostics(t *testing.T) {
	err := errors.Join(Wrap("transport", "", &net.OpError{Op: "read", Net: "tcp", Err: io.EOF}), fmt.Errorf("WRAPPED_PATH: %w", context.DeadlineExceeded), backendCause{})
	causes := Causes(err)
	if len(causes) != 7 || causes[2].Type != "*net.OpError" || causes[3].Reason != "peer_closed" || causes[5].Reason != "timeout" || causes[6].Parent != 0 {
		t.Fatalf("causes=%+v", causes)
	}
	data, err := json.Marshal(causes)
	// The graph is types and classes; the text is the final record's.
	if err != nil || strings.Contains(string(data), "WRAPPED_PATH") || strings.Contains(string(data), "BACKEND_TEXT") {
		t.Fatalf("error text in the cause graph: %s %v", data, err)
	}
}

func TestKnownCauseNames(t *testing.T) {
	for _, test := range []struct {
		err    error
		reason string
	}{{session.ErrClosed, "owner_closed"}, {errors.ErrUnsupported, "unsupported"}} {
		causes := Causes(test.err)
		if len(causes) != 1 || causes[0].Reason != test.reason {
			t.Fatalf("causes=%+v want=%s", causes, test.reason)
		}
	}
}
