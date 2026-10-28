//go:build gui

package ui

import (
	"errors"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type unprintableWindowError struct{}

func (unprintableWindowError) Error() string { panic("window errors must never be formatted") }

func TestWindowErrorReporting(t *testing.T) {
	for _, err := range []error{nil, errors.New("https://example.invalid/#private"), unprintableWindowError{}, &application.FatalError{}} {
		var shown []string
		handler := fixedWindowErrorHandler(func(message string) { shown = append(shown, message) })
		handler(err)
		if len(shown) != 1 || shown[0] != "The window engine could not start. On Windows, WebView2 Runtime is required. Install or repair the runtime and try again." {
			t.Fatalf("missing fixed engine diagnostic: %q", shown)
		}
	}
}
