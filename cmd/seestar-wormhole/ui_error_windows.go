//go:build gui && !guitest

package main

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

func showUIStartupError(err error) {
	title, _ := windows.UTF16PtrFromString("seestar-wormhole")
	message, _ := windows.UTF16PtrFromString(err.Error())
	windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(message)), uintptr(unsafe.Pointer(title)), 0x10)
}
