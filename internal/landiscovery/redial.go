package landiscovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
)

// Resolved is how [Redial] reached a telescope. Endpoint is the address it
// connected to; Moved reports that it differs from the saved one. Seen is
// set when a scan found the telescope, so its model can be logged; Replies
// is that scan's reply count.
type Resolved struct {
	Endpoint string
	Moved    bool
	Seen     *Telescope
	Replies  int
}

// Redial connects to a telescope saved as endpoint (host:port) and serial.
// It tries endpoint first. When that connect gets no answer or is refused
// and a serial is saved, it scans for the serial and tries each address
// the telescope answered from, on the saved port. When nothing better is
// found it returns the first failure.
func Redial[T any](ctx context.Context, s *Scanner, endpoint, serial string, dial func(context.Context, string) (T, error)) (T, Resolved, error) {
	session, err := dial(ctx, endpoint)
	if err == nil || serial == "" || s == nil || ctx.Err() != nil || !Unreached(err) {
		return session, Resolved{Endpoint: endpoint}, err
	}
	host, port, splitErr := net.SplitHostPort(endpoint)
	if splitErr != nil {
		return session, Resolved{Endpoint: endpoint}, err
	}
	result, scanErr := s.Find(ctx, serial)
	telescope, ok := result.Find(serial)
	if scanErr != nil || !ok {
		return session, Resolved{Endpoint: endpoint, Replies: result.Replies}, err
	}
	for _, address := range telescope.Addresses {
		if saved, parseErr := netip.ParseAddr(host); parseErr == nil && saved.Unmap() == address {
			continue
		}
		moved := net.JoinHostPort(address.String(), port)
		if next, nextErr := dial(ctx, moved); nextErr == nil {
			return next, Resolved{Endpoint: moved, Moved: true, Seen: &telescope, Replies: result.Replies}, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return session, Resolved{Endpoint: endpoint, Seen: &telescope, Replies: result.Replies}, err
}

// Unreached reports whether err is a connect that never reached a server:
// it timed out, was refused, or had no route. Those are the failures a
// changed address causes. An answer from a server is not one.
func Unreached(err error) bool {
	op, ok := errors.AsType[*net.OpError](err)
	return ok && op.Op == "dial"
}
