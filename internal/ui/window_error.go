//go:build gui

package ui

func fixedWindowErrorHandler(show func(string)) func(error) {
	return func(error) {
		show("The window engine could not start. On Windows, WebView2 Runtime is required. Install or repair the runtime and try again.")
	}
}
