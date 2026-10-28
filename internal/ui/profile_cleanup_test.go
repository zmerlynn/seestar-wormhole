package ui

import (
	"errors"
	"strings"
	"testing"
)

func TestProfileCleanupWarningPreservesResult(t *testing.T) {
	for _, runErr := range []error{nil, errors.New("window failed")} {
		var out strings.Builder
		got := profileCleanupResult(&out, func() error {
			return errors.New("WebView2 profile cleanup failed")
		}, runErr)
		if got != runErr {
			t.Fatalf("cleanup changed window result: %v", got)
		}
		if !strings.Contains(out.String(), "Warning: WebView2 profile cleanup failed") {
			t.Fatal("missing cleanup warning")
		}
	}
	var out strings.Builder
	if err := profileCleanupResult(&out, func() error { return nil }, nil); err != nil || out.Len() != 0 {
		t.Fatal("successful cleanup warned or failed", err)
	}
}
