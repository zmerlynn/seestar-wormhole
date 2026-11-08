package diagnostic

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"syscall"
	"testing"
)

func TestCode(t *testing.T) {
	for _, c := range []struct {
		source, op string
		failed     bool
		want       string
	}{
		{SourceBrowse, OpConnect, true, "browse_connect_failed"},
		{SourceBrowse, OpConnect, false, "browse_connect_recovered"},
		{SourceCopy, OpOtherTelescope, true, "copy_other_telescope_failed"},
		{SourceDelete, OpSaveOptions, false, "delete_save_options_recovered"},
	} {
		if got := Code(c.source, c.op, c.failed); got != c.want {
			t.Errorf("Code(%q, %q, %v) = %q, want %q", c.source, c.op, c.failed, got, c.want)
		}
	}
}

func TestOpFields(t *testing.T) {
	refused := fmt.Errorf("connect to HOST-SENTINEL: %w", syscall.ECONNREFUSED)
	for _, c := range []struct {
		name          string
		op            string
		err           error
		phase, reason string
	}{
		{"nil", OpPass, nil, "pass", "none"},
		{"op is the phase", OpRead, fs.ErrPermission, "read", "permission_denied"},
		{"At sets the phase", OpPass, At("connect", refused), "connect", "connection_refused"},
		{"At wrapped further", OpPass, fmt.Errorf("pass: %w", At("connect", refused)), "connect", "connection_refused"},
		{"Wrap wins", OpStart, Wrap("open", "busy", errors.New("held")), "open", "busy"},
		{"timeout", OpConnect, fmt.Errorf("dial: %w", context.DeadlineExceeded), "connect", "timeout"},
		{"not found", OpPass, fmt.Errorf("%w: /PATH-SENTINEL: %w", errors.New("destination missing"), fs.ErrNotExist), "pass", "not_found"},
	} {
		t.Run(c.name, func(t *testing.T) {
			phase, reason := OpFields(c.op, c.err)
			if phase != c.phase || reason != c.reason {
				t.Errorf("OpFields = %q, %q, want %q, %q", phase, reason, c.phase, c.reason)
			}
		})
	}
}

func TestAtKeepsText(t *testing.T) {
	err := errors.New("connect to HOST-SENTINEL: refused")
	if got := At("connect", err).Error(); got != err.Error() {
		t.Errorf("At changed the text to %q", got)
	}
	if !errors.Is(At("connect", err), err) {
		t.Error("At hides its cause")
	}
	if At("connect", nil) != nil {
		t.Error("At(nil) is not nil")
	}
}

func TestOpKeysHoldNoNames(t *testing.T) {
	err := At("connect", fmt.Errorf("connect to HOST-SENTINEL for /PATH-SENTINEL/FILE-SENTINEL: %w", syscall.ECONNREFUSED))
	keys := OpKeys(SourceCopy, OpPass, err)
	want := []any{"source", "copy", "op", "pass", "phase", "connect", "reason", "connection_refused"}
	if !slices.Equal(keys, want) {
		t.Errorf("OpKeys = %v, want %v", keys, want)
	}
}
