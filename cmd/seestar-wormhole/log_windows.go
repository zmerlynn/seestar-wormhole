//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"io"
	"os"
)

type failedLog struct{ err error }

func (w failedLog) Write([]byte) (int, error) { return 0, w.err }

// consoleLog exposes the worker-owned duplicate only for x/term console queries.
// Writes and handle closure still belong to queuedLog's worker.
type consoleLog struct {
	*queuedLog
	fd uintptr
}

func (w consoleLog) Fd() uintptr { return w.fd }

func logWriter() (io.Writer, func()) { return logWriterFD(2) }

func logWriterFD(descriptor int) (io.Writer, func()) {
	handle := uint32(windows.STD_ERROR_HANDLE)
	if descriptor == 1 {
		handle = uint32(windows.STD_OUTPUT_HANDLE)
	}
	h, err := windows.GetStdHandle(handle)
	if err != nil {
		return failedLog{err}, func() {}
	}
	var duplicate windows.Handle
	process := windows.CurrentProcess()
	if err := windows.DuplicateHandle(process, h, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return failedLog{err}, func() {}
	}
	w := newQueuedLog(os.NewFile(uintptr(duplicate), "stderr"))
	return consoleLog{queuedLog: w, fd: uintptr(duplicate)}, w.close
}
