//go:build gui

package ui

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Wails calls this handler for fatal errors before exiting, including WebView2
// initialization failure. Use MessageBox directly; no window dispatcher exists yet.
var windowErrorHandler = fixedWindowErrorHandler(func(text string) {
	title, _ := windows.UTF16PtrFromString("seestar-wormhole")
	message, _ := windows.UTF16PtrFromString(text)
	windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x10)
})
