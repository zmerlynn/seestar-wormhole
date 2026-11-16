//go:build gui

package ui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#include <stdbool.h>
#import <Cocoa/Cocoa.h>

// The window's web view: Wails' window keeps it as its webView.
static NSView *webViewOf(void *window) {
	NSWindow *w = (NSWindow *)window;
	if (![w respondsToSelector:@selector(webView)]) return nil;
	return (NSView *)[w performSelector:@selector(webView)];
}

static void focusWebView(void *window) {
	NSWindow *w = (NSWindow *)window;
	NSView *view = webViewOf(window);
	if (view != nil && [w firstResponder] != view) [w makeFirstResponder:view];
}

static bool webViewFocused(void *window) {
	NSView *view = webViewOf(window);
	return view != nil && [(NSWindow *)window firstResponder] == view;
}
*/
import "C"

import "github.com/wailsapp/wails/v3/pkg/application"

// focusWebView makes the window's web view its first responder, as a click
// into it would, so its page has focus whenever the window does. Nothing
// else does that for a WKWebView that is a subview of the window's content,
// and without it a page's focus calls and events wait for a click. It
// neither activates the app nor orders the window.
//
// The window is looked up on the main thread, where Wails destroys it, so
// it cannot be freed between the lookup and its use.
func focusWebView(w *application.WebviewWindow) {
	if w == nil {
		return
	}
	application.InvokeAsync(func() {
		if p := w.NativeWindow(); p != nil {
			C.focusWebView(p)
		}
	})
}

// webViewFocused tells whether the window's web view is its first
// responder, for the GUI fixture's diagnostics.
func webViewFocused(w *application.WebviewWindow) bool {
	if w == nil {
		return false
	}
	var focused bool
	application.InvokeSync(func() {
		if p := w.NativeWindow(); p != nil {
			focused = bool(C.webViewFocused(p))
		}
	})
	return focused
}
