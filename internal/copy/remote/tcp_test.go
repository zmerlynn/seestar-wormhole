package remote

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
)

// refusedEndpoint is a loopback endpoint nothing listens on.
func refusedEndpoint(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := l.Addr().String()
	l.Close()
	return endpoint
}

func TestTCPErrorsNameNoAddress(t *testing.T) {
	endpoint := refusedEndpoint(t)
	_, port, _ := net.SplitHostPort(endpoint)
	_, err := TCP(endpoint)(t.Context())
	if err == nil {
		t.Fatal("dialed a closed port")
	}
	op, ok := errors.AsType[*net.OpError](err)
	if !ok || op.Op != "dial" || !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("error %#v lost what failed", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), port) {
		t.Fatalf("error %q names the address", err)
	}

	// Through DialVia, as a serial-bound connect makes it.
	_, err = DialVia(t.Context(), "the saved telescope", TCP(endpoint), nil)
	if err == nil || strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), port) ||
		!strings.Contains(err.Error(), "the saved telescope") || !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("DialVia error %q", err)
	}
}

// addressedConn fails every read and write with an error naming addresses.
type addressedConn struct{ net.Conn }

func (addressedConn) Read([]byte) (int, error) {
	return 0, &net.OpError{Op: "read", Net: "tcp", Source: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 5), Port: 50000},
		Addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 30), Port: 445}, Err: os.NewSyscallError("read", syscall.ECONNRESET)}
}

func (addressedConn) Write([]byte) (int, error) {
	return 0, &net.OpError{Op: "write", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 30), Port: 445}, Err: os.ErrDeadlineExceeded}
}

func TestTCPStreamErrorsNameNoAddress(t *testing.T) {
	c := unaddressedConn{addressedConn{}}
	_, rerr := c.Read(make([]byte, 1))
	_, werr := c.Write([]byte{0})
	for _, err := range []error{rerr, werr} {
		if strings.Contains(err.Error(), "192.0.2.") || strings.Contains(err.Error(), "445") {
			t.Fatalf("stream error %q names an address", err)
		}
	}
	if !errors.Is(rerr, syscall.ECONNRESET) {
		t.Fatalf("read error %v lost its cause", rerr)
	}
	if ne, ok := werr.(net.Error); !ok || !ne.Timeout() || !errors.Is(werr, os.ErrDeadlineExceeded) {
		t.Fatalf("write error %v is no longer a timeout", werr)
	}
	// A clean end of stream stays io.EOF-comparable: unaddressed passes
	// other errors through untouched.
	if err := unaddressed(nil); err != nil {
		t.Fatal(err)
	}
}

// noDNS makes the default resolver's lookups fail, without sending any,
// for the rest of the test: its server answers with an error naming the
// server.
func noDNS(t *testing.T) {
	saved := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = saved })
	net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("resolver 198.51.100.53 unreachable")
	}}
}

// A lookup's error, whether it names the host, the server or neither,
// says only that the lookup failed, and stays the lookup error it was.
func TestDNSFailureNamesNoHost(t *testing.T) {
	const host = "telescope.invalid"
	for _, c := range []struct {
		cause                     *net.DNSError
		text                      string
		timeout, temporary, found bool
	}{
		{&net.DNSError{Err: "no such host", Name: host, Server: "198.51.100.53:53", IsNotFound: true}, "name lookup: no such host", false, false, true},
		{&net.DNSError{Err: "i/o timeout", Name: host, Server: "198.51.100.53:53", IsTimeout: true, IsTemporary: true}, "name lookup: timed out", true, true, false},
		{&net.DNSError{Err: "server misbehaving", Name: host, Server: "198.51.100.53:53", IsTemporary: true}, "name lookup failed", false, true, false},
	} {
		conn := func(context.Context) (net.Conn, error) {
			return nil, unaddressed(&net.OpError{Op: "dial", Net: "tcp", Err: c.cause})
		}
		_, err := DialVia(t.Context(), "the saved telescope", conn, nil)
		if err == nil {
			t.Fatal("wanted a lookup failure")
		}
		if text := err.Error(); strings.Contains(text, host) || strings.Contains(text, "198.51.100.53") ||
			!strings.Contains(text, c.text) || !strings.Contains(text, "the saved telescope") {
			t.Fatalf("error %q", text)
		}
		dns, ok := errors.AsType[*net.DNSError](err)
		if !ok || dns != c.cause {
			t.Fatalf("error %v doesn't unwrap to its lookup error", err)
		}
		ne, ok := errors.AsType[net.Error](err)
		if !ok || ne.Timeout() != c.timeout {
			t.Fatalf("error %v: timeout %v, want %v", err, ok && ne.Timeout(), c.timeout)
		}
		if op, ok := errors.AsType[*net.OpError](err); !ok || op.Op != "dial" || op.Temporary() != c.temporary {
			t.Fatalf("error %v lost what failed", err)
		}
		if got := unnamed(unnamed(c.cause)); got.Error() != c.text {
			t.Fatalf("hidden twice: %q", got)
		}
	}
	// An address that doesn't parse names nothing either.
	for _, cause := range []error{&net.AddrError{Err: "missing port in address", Addr: host}, &net.ParseError{Type: "IP address", Text: host}} {
		err := unaddressed(&net.OpError{Op: "dial", Net: "tcp", Err: cause})
		if strings.Contains(err.Error(), host) || !errors.Is(err, cause) {
			t.Fatalf("error %q", err)
		}
	}
	// A real dial of a hostname, its lookup failing without a query sent.
	noDNS(t)
	_, err := DialVia(t.Context(), "the saved telescope", TCP(host+":445"), nil)
	if err == nil {
		t.Fatal("wanted a lookup failure")
	}
	if text := err.Error(); strings.Contains(text, host) || strings.Contains(text, "198.51.100.53") || strings.Contains(text, ":53") || !strings.Contains(text, "name lookup") {
		t.Fatalf("dial error %q", text)
	}
	if _, ok := errors.AsType[*net.DNSError](err); !ok {
		t.Fatalf("dial error %v isn't a lookup error", err)
	}
}
