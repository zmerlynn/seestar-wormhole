//go:build windows

// Package oserror matches portable errno labels against native OS errors.
package oserror

import (
	"errors"
	"golang.org/x/sys/windows"
	"syscall"
)

// Is recognizes Winsock/Win32 errors as well as portable errno test fixtures.
// It never formats untrusted errors or changes the original error chain.
func Is(err, target error) bool {
	if errors.Is(err, target) {
		return true
	}
	var native []error
	switch target {
	case syscall.EACCES:
		native = []error{windows.WSAEACCES}
	case syscall.ECONNREFUSED:
		native = []error{windows.WSAECONNREFUSED}
	case syscall.ECONNRESET:
		native = []error{windows.WSAECONNRESET, windows.ERROR_NETNAME_DELETED}
	case syscall.EADDRINUSE:
		native = []error{windows.WSAEADDRINUSE}
	case syscall.EADDRNOTAVAIL:
		native = []error{windows.WSAEADDRNOTAVAIL}
	case syscall.EPIPE:
		native = []error{windows.ERROR_BROKEN_PIPE, windows.WSAESHUTDOWN}
	case syscall.ENOTDIR:
		native = []error{windows.ERROR_DIRECTORY}
	case syscall.ELOOP:
		native = []error{windows.ERROR_CANT_ACCESS_FILE}
	case syscall.EMFILE:
		native = []error{windows.WSAEMFILE, windows.ERROR_TOO_MANY_OPEN_FILES}
	case syscall.ENOSPC:
		native = []error{windows.ERROR_DISK_FULL, windows.ERROR_HANDLE_DISK_FULL}
	case syscall.EROFS:
		native = []error{windows.ERROR_WRITE_PROTECT}
	}
	for _, cause := range native {
		if errors.Is(err, cause) {
			return true
		}
	}
	return false
}
