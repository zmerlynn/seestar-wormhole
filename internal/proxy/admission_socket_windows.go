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
	// Control pins the descriptor only for this nonblocking query. SO_ERROR
	// gets and clears errors (Winsock SO_ERROR); it consumes no queued data. A valid
	// FIN has no socket error, including with an unread prefix.
	err := s.raw.Control(func(fd uintptr) {
		pending, queryErr = windows.GetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, soError)
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
