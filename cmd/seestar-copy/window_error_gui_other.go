//go:build gui && !windows

package main

// windowErrorHandler is Wails' default elsewhere: the fatal webview error
// it guards against is WebView2's.
var windowErrorHandler func(error)
