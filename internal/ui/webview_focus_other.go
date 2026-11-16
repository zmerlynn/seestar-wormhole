//go:build gui && !darwin

package ui

import "github.com/wailsapp/wails/v3/pkg/application"

// WebView2 and WebKitGTK take focus with their window.
func focusWebView(*application.WebviewWindow) {}

func webViewFocused(*application.WebviewWindow) bool { return false }
