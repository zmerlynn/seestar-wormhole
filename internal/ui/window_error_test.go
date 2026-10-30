//go:build gui

package ui

import (
	"errors"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestWindowErrorReporting(t *testing.T) {
	for _, tc := range []struct {
		err   error
		shown bool
	}{
		{nil, false},
		{errors.New("https://example.invalid/#private"), false},
		{&application.FatalError{}, true},
	} {
		var shown []string
		handler := fixedWindowErrorHandler(func(message string) { shown = append(shown, message) })
		handler(tc.err)
		if !tc.shown {
			if len(shown) != 0 {
				t.Fatalf("non-fatal error shown: %q", shown)
			}
			continue
		}
		if len(shown) != 1 || shown[0] != "Could not open the app window. Install or repair Microsoft Edge WebView2 Runtime, then try again." {
			t.Fatalf("missing fixed engine diagnostic: %q", shown)
		}
	}
}
