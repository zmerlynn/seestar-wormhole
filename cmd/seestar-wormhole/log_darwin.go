//go:build darwin

package main

import (
	"golang.org/x/sys/unix"
	"io"
)

type rawLog int

func (w rawLog) Write(b []byte) (int, error) { n, err := unix.Write(int(w), b); return max(n, 0), err }

// Fd preserves terminal detection and width queries without bypassing Write.
func (w rawLog) Fd() uintptr { return uintptr(w) }

type failedLog struct{ err error }

func (w failedLog) Write([]byte) (int, error) { return 0, w.err }

// Darwin has no /proc/self/fd reopen with an independent file description.
// Temporarily make inherited stderr nonblocking so a full pipe cannot stall
// shutdown. The duplicate shares status flags; restore those flags on exit.
func logWriter() (io.Writer, func()) { return logWriterFD(2) }

func logWriterFD(descriptor int) (io.Writer, func()) {
	flags, err := unix.FcntlInt(uintptr(descriptor), unix.F_GETFL, 0)
	if err != nil {
		return failedLog{err}, func() {}
	}
	fd, err := unix.FcntlInt(uintptr(descriptor), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return failedLog{err}, func() {}
	}
	if _, err = unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags|unix.O_NONBLOCK); err != nil {
		unix.Close(fd)
		return failedLog{err}, func() {}
	}
	w := &logLossWriter{output: rawLog(fd)}
	return w, func() {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags); err != nil {
			w.lose(0)
		}
		if unix.Close(fd) != nil {
			w.lose(0)
		}
	}
}
