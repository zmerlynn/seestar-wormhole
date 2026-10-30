//go:build gui

package ui

import (
	"errors"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Wails also passes non-fatal runtime and WebView2 errors to its handler; only a
// fatal error, which exits right after, gets the message.
func fixedWindowErrorHandler(show func(string)) func(error) {
	return func(err error) {
		var fatal *application.FatalError
		if errors.As(err, &fatal) {
			show("Could not open the app window. Install or repair Microsoft Edge WebView2 Runtime, then try again.")
		}
	}
}
