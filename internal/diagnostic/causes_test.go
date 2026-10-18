package diagnostic

import (
	"context"
	"errors"
	"fmt"
	smb2 "github.com/cloudsoda/go-smb2"
	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"io"
	"strings"
	"testing"
)

type privateCause struct{}

func (privateCause) Error() string { panic("private backend error formatted") }
func TestPullCauseDiagnostics(t *testing.T) {
	err := errors.Join(Wrap("smb_shutdown", "", &smb2.TransportError{Err: io.EOF}), fmt.Errorf("PRIVATE_PATH: %w", context.DeadlineExceeded), privateCause{})
	causes := Causes(err)
	if len(causes) != 7 || causes[2].Type != "*smb2.TransportError" || causes[3].Reason != "peer_closed" || causes[5].Reason != "timeout" || causes[6].Parent != 0 {
		t.Fatalf("causes=%+v", causes)
	}
	data, err := json.Marshal(causes)
	if err != nil || strings.Contains(string(data), "PRIVATE_PATH") {
		t.Fatalf("private text in diagnostics: %s %v", data, err)
	}
}

func TestPullKnownCauseNames(t *testing.T) {
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
