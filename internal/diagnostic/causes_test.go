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

type privateCause struct{}

func (privateCause) Error() string { panic("private backend error formatted") }
func TestCauseDiagnostics(t *testing.T) {
	err := errors.Join(Wrap("transport", "", &net.OpError{Op: "read", Net: "tcp", Err: io.EOF}), fmt.Errorf("PRIVATE_PATH: %w", context.DeadlineExceeded), privateCause{})
	causes := Causes(err)
	if len(causes) != 7 || causes[2].Type != "*net.OpError" || causes[3].Reason != "peer_closed" || causes[5].Reason != "timeout" || causes[6].Parent != 0 {
		t.Fatalf("causes=%+v", causes)
	}
	data, err := json.Marshal(causes)
	if err != nil || strings.Contains(string(data), "PRIVATE_PATH") {
		t.Fatalf("private text in diagnostics: %s %v", data, err)
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
