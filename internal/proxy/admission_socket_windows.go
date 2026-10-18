//go:build windows

package proxy

import (
	"net"
	"syscall"

	"golang.org/x/sys/windows"
)

// Winsock SO_ERROR (winsock2.h); x/sys/windows does not export this option.
const soError = 0x1007

type tcpSocketErrors struct{ raw syscall.RawConn }

func admissionSocketErrors(conn net.Conn) (socketErrorSource, error) {
	if source, ok := conn.(socketErrorSource); ok {
		return source, nil
	}
	c, ok := conn.(syscall.Conn)
	if !ok {
		return nil, nil
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, err
	}
	return tcpSocketErrors{raw}, nil
}

func (s tcpSocketErrors) socketError() error {
	var pending int
	var queryErr error
	// Winsock can leave SO_ERROR clear after an abort with unread bytes. Also
	// query whether this established socket is still connected. A valid FIN
	// leaves the reverse stream connected; neither query consumes queued bytes.
	err := s.raw.Control(func(fd uintptr) {
		pending, queryErr = windows.GetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, soError)
		if queryErr == nil && pending == 0 {
			_, queryErr = windows.Getpeername(windows.Handle(fd))
		}
	})
	if err != nil {
		return err
	}
	if queryErr != nil {
		return queryErr
	}
	if pending != 0 {
		return syscall.Errno(pending)
	}
	return nil
}
