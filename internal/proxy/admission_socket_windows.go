//go:build windows

package proxy

import (
	"encoding/binary"
	"io"
	"net"
	"syscall"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"golang.org/x/sys/windows"
)

// Winsock constants from winsock2.h and mstcpip.h, absent from x/sys/windows.
const (
	soError        = 0x1007
	sioTCPInfo     = 0xd8000027
	tcpStateClosed = 0
)

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
	// Winsock leaves SO_ERROR clear and retains Getpeername after a reset with
	// unread bytes. TCP_INFO_v0 exposes CLOSED after RST and CLOSE_WAIT after
	// FIN without consuming bytes. These accepted sockets were established;
	// CLOSED is terminal, while FIN must preserve the queued stream and reply.
	err := s.raw.Control(func(fd uintptr) {
		pending, queryErr = windows.GetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, soError)
		if queryErr == nil && pending == 0 {
			// SIO_TCP_INFO takes a DWORD version (zero) and an 88-byte v0
			// structure. Only its first DWORD, TCPSTATE, is needed here.
			var version [4]byte
			var info [88]byte
			var returned uint32
			queryErr = windows.WSAIoctl(windows.Handle(fd), sioTCPInfo,
				&version[0], uint32(len(version)), &info[0], uint32(len(info)), &returned, nil, 0)
			if queryErr != nil {
				queryErr = diagnostic.Wrap("tcp_state_query", "", queryErr)
			} else if returned != uint32(len(info)) {
				queryErr = diagnostic.Wrap("tcp_state_query", "", io.ErrUnexpectedEOF)
			} else if binary.LittleEndian.Uint32(info[:4]) == tcpStateClosed {
				queryErr = windows.WSAECONNRESET
			}
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
