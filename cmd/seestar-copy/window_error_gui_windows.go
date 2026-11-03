//go:build gui

package main

import (
	"errors"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows"
)

// windowErrorMessage is shown when the webview can't start.
const windowErrorMessage = "Couldn't open the window. Install or repair Microsoft Edge WebView2 Runtime, then reopen seestar-copy."

// windowErrorHandler is Wails' error handler. Wails passes it runtime and
// WebView2 errors that are not fatal too; only a fatal one, after which it
// exits, is shown, with MessageBox, since no window exists to show it in.
func windowErrorHandler(err error) {
	var fatal *application.FatalError
	if errors.As(err, &fatal) {
		messageBox("seestar-copy", windowErrorMessage)
	}
}

// messageBox shows an error with MessageBox, which needs no window.
func messageBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10)
}
