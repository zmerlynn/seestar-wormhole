package remote

import (
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
