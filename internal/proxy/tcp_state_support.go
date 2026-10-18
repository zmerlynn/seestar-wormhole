package proxy

import (
	"context"
	"errors"
	"net"
	"runtime"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// CheckTCPStateSupport verifies Windows admission's non-consuming state query
// before forwarding starts. Other platforms do not require SIO_TCP_INFO.
func CheckTCPStateSupport(ctx context.Context) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	return checkTCPStateSupport(ctx, func(conn net.Conn) error {
		source, err := admissionSocketErrors(conn)
		if err != nil {
			return err
		}
		if source == nil {
			return errors.New("TCP state query unavailable")
		}
		return source.socketError()
	})
}

func checkTCPStateSupport(ctx context.Context, query func(net.Conn) error) error {
	// Bound setup even when loopback is filtered or unavailable. Both endpoints
	// stay connected until the accepted socket has been inspected, as in admission.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return diagnostic.Wrap("tcp_state_loopback", "loopback_unavailable", err)
	}
	defer l.Close()
	deadline, _ := ctx.Deadline()
	if err := l.SetDeadline(deadline); err != nil {
		return diagnostic.Wrap("tcp_state_loopback", "loopback_unavailable", err)
	}
	peer, err := (&net.Dialer{}).DialContext(ctx, "tcp4", l.Addr().String())
	if err != nil {
		return diagnostic.Wrap("tcp_state_loopback", "loopback_unavailable", err)
	}
	defer peer.Close()
	conn, err := l.AcceptTCP()
	if err != nil {
		return diagnostic.Wrap("tcp_state_loopback", "loopback_unavailable", err)
	}
	defer conn.Close()
	return diagnostic.Wrap("tcp_state_query", "", query(conn))
}
