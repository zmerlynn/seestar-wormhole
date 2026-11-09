package remote

import (
	"context"
	"errors"
	"net"
)

// TCP is a Conn that dials endpoint (host:port) over TCP. Its errors, and
// those of the stream's reads and writes, name no address, so an address a
// scan found stays out of error text and logs; give DialVia a name that
// doesn't carry it either.
func TCP(endpoint string) Conn {
	return func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		c, err := d.DialContext(ctx, "tcp", endpoint)
		if err != nil {
			return nil, unaddressed(err)
		}
		return unaddressedConn{c}, nil
	}
}

// unaddressed is err with the addresses a *net.OpError names removed. Its
// Op, Net and Err stay, so errors.Is and the timeout and connect checks
// still see what failed.
func unaddressed(err error) error {
	if op, ok := errors.AsType[*net.OpError](err); ok {
		return &net.OpError{Op: op.Op, Net: op.Net, Err: op.Err}
	}
	return err
}

// unaddressedConn is a stream whose read and write errors name no address.
// Its LocalAddr and RemoteAddr are for code, never for text.
type unaddressedConn struct {
	net.Conn
}

func (c unaddressedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	return n, unaddressed(err)
}

func (c unaddressedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	return n, unaddressed(err)
}
