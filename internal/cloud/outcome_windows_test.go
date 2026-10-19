//go:build windows

package cloud

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"testing"
)

func TestWindowsRefusedOutcome(t *testing.T) {
	err := JoinOutcomes(fmt.Errorf("private sentinel: %w", windows.WSAECONNREFUSED))
	var outcome *Outcome
	if !errors.As(err, &outcome) || outcome.Severity() != Retryable {
		t.Fatalf("refusal severity: %v", err)
	}
}
