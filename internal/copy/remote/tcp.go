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

// unaddressed is err with the addresses a *net.OpError names removed, and
// a cause that would name a host or a resolver given fixed text (see
// [unnamed]). Its Op, Net and Err stay, so errors.Is and the timeout and
// connect checks still see what failed.
func unaddressed(err error) error {
	if op, ok := errors.AsType[*net.OpError](err); ok {
		return &net.OpError{Op: op.Op, Net: op.Net, Err: unnamed(op.Err)}
	}
	return unnamed(err)
}

// unnamed is err, or, when err is a cause whose text names the host it
// looked up, the server it asked or the address it parsed, an error with
// fixed text in its place. That error unwraps to err and keeps its
// timeout and temporary classification.
func unnamed(err error) error {
	switch e := err.(type) {
	case *net.DNSError:
		text := "name lookup failed"
		switch {
		case e.IsNotFound:
			text = "name lookup: no such host"
		case e.IsTimeout:
			text = "name lookup: timed out"
		}
		return &fixedText{text, err}
	case *net.AddrError, *net.ParseError:
		return &fixedText{"invalid address", err}
	}
	return err
}

// fixedText is an error whose text is text, in place of err's.
type fixedText struct {
	text string
	err  error
}

func (e *fixedText) Error() string { return e.text }
func (e *fixedText) Unwrap() error { return e.err }

// Timeout and Temporary are err's, so that fixedText stays a net.Error
// that classifies as err does.
func (e *fixedText) Timeout() bool {
	t, ok := e.err.(interface{ Timeout() bool })
	return ok && t.Timeout()
}

func (e *fixedText) Temporary() bool {
	t, ok := e.err.(interface{ Temporary() bool })
	return ok && t.Temporary()
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
