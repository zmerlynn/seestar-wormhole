//go:build linux

package main

import (
	"golang.org/x/sys/unix"
	"io"
)

// rawLog uses a separate nonblocking stderr descriptor. A full pipe drops the
// line instead of blocking the owner or shutdown. No log goroutine needs joining.
type rawLog int

func (w rawLog) Write(b []byte) (int, error) { return unix.Write(int(w), b) }

// Fd preserves terminal detection and width queries without bypassing Write.
func (w rawLog) Fd() uintptr { return uintptr(w) }

// MSG_DONTWAIT affects only this send, unlike O_NONBLOCK on a dup which
// would change the inherited descriptor's shared open-file description.
type socketLog int

func (w socketLog) Fd() uintptr { return uintptr(w) }

func (w socketLog) Write(b []byte) (int, error) {
	return unix.SendmsgN(int(w), b, nil, nil, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
}

type failedLog struct{ err error }

func (w failedLog) Write([]byte) (int, error) { return 0, w.err }

func logWriter() (io.Writer, func()) {
	var st unix.Stat_t
	if err := unix.Fstat(2, &st); err != nil {
		return failedLog{err}, func() {}
	}
	if st.Mode&unix.S_IFMT == unix.S_IFSOCK {
		fd, err := unix.FcntlInt(uintptr(2), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			return failedLog{err}, func() {}
		}
		return socketLog(fd), func() { unix.Close(fd) }
	}
	fd, err := unix.Open("/proc/self/fd/2", unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return failedLog{err}, func() {}
	}
	return rawLog(fd), func() { unix.Close(fd) }
}
