//go:build linux

package main

import (
	"golang.org/x/sys/unix"
	"io"
	"strconv"
)

// rawLog uses a separate nonblocking stderr descriptor. A full pipe drops the
// line instead of blocking the owner or shutdown. No log goroutine needs joining.
type rawLog int

func (w rawLog) Write(b []byte) (int, error) { n, err := unix.Write(int(w), b); return max(n, 0), err }

// Fd preserves terminal detection and width queries without bypassing Write.
func (w rawLog) Fd() uintptr { return uintptr(w) }

// MSG_DONTWAIT affects only this send, unlike O_NONBLOCK on a dup which
// would change the inherited descriptor's shared open-file description.
type socketLog int

func (w socketLog) Fd() uintptr { return uintptr(w) }

func (w socketLog) Write(b []byte) (int, error) {
	n, err := unix.SendmsgN(int(w), b, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
	return max(n, 0), err
}

type failedLog struct{ err error }

func (w failedLog) Write([]byte) (int, error) { return 0, w.err }

func logWriter() (io.Writer, func()) { return logWriterFD(2) }

func logWriterFD(descriptor int) (io.Writer, func()) {
	var st unix.Stat_t
	if err := unix.Fstat(descriptor, &st); err != nil {
		return failedLog{err}, func() {}
	}
	if st.Mode&unix.S_IFMT == unix.S_IFSOCK {
		fd, err := unix.FcntlInt(uintptr(descriptor), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return failedLog{err}, func() {}
		}
		w := &logLossWriter{output: socketLog(fd)}
		return w, func() {
			if unix.Close(fd) != nil {
				w.lose(0)
			}
		}
	}
	fd, err := unix.Open("/proc/self/fd/"+strconv.Itoa(descriptor), unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return failedLog{err}, func() {}
	}
	w := &logLossWriter{output: rawLog(fd)}
	return w, func() {
		if unix.Close(fd) != nil {
			w.lose(0)
		}
	}
}
