//go:build gui

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

// A windowsgui executable has no console by default. Arguments opt in to its
// parent's console; Explorer launches remain window-only. AttachConsole may
// replace process standard handles, so restore every valid inherited handle.
func init() {
	if len(os.Args) <= 1 {
		return
	}
	stdout, stdoutValid := inheritedOutput(windows.STD_OUTPUT_HANDLE)
	stderr, stderrValid := inheritedOutput(windows.STD_ERROR_HANDLE)
	attach := windows.NewLazySystemDLL("kernel32.dll").NewProc("AttachConsole")
	attach.Call(uintptr(^uint32(0))) // ATTACH_PARENT_PROCESS; already attached is OK.
	restoreConsoleOutput(windows.STD_OUTPUT_HANDLE, stdout, stdoutValid, &os.Stdout)
	restoreConsoleOutput(windows.STD_ERROR_HANDLE, stderr, stderrValid, &os.Stderr)
}

func inheritedOutput(id uint32) (windows.Handle, bool) {
	handle, err := windows.GetStdHandle(id)
	if err != nil || handle == 0 || handle == windows.InvalidHandle {
		return handle, false
	}
	_, err = windows.GetFileType(handle)
	return handle, err == nil
}

func restoreConsoleOutput(id uint32, handle windows.Handle, valid bool, output **os.File) {
	if valid {
		_ = windows.SetStdHandle(id, handle)
		return
	}
	file, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	if err := windows.SetStdHandle(id, windows.Handle(file.Fd())); err != nil {
		file.Close()
		return
	}
	*output = file
}
