package proxy

import (
	"context"
	"net"
	"testing"
	"testing/synctest"
)

type admissionWatchConn struct {
	net.Conn
	checking, release chan struct{}
}

func (c *admissionWatchConn) socketError() error {
	close(c.checking)
	<-c.release
	return nil
}

type admissionBridgeReadConn struct {
	net.Conn
	reading chan struct{}
}

func (c *admissionBridgeReadConn) Read(p []byte) (int, error) {
	select {
	case c.reading <- struct{}{}:
	default:
	}
	return c.Conn.Read(p)
}

// Deliberately hold an in-progress socket check. All exit paths must join it;
// a successful dial must also join before bridge I/O can consume socket errors.
func TestAdmissionJoinsSocketWatcher(t *testing.T) {
	for _, path := range []string{"shutdown", "dial-rejection", "handoff"} {
		t.Run(path, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				a, peer := net.Pipe()
				defer peer.Close()
				local := &admissionWatchConn{a, make(chan struct{}), make(chan struct{})}
				l := &e2eSingleListener{conn: local, done: make(chan struct{})}
				d := admissionGateDial{make(chan admissionDialRequest)}
				finished := make(chan error, 1)
				go func() { finished <- Serve(ctx, d, []Forward{{Listener: l, Port: 445}}, 1, nil) }()
				req := <-d.requests
				<-local.checking
				remote, device := net.Pipe()
				defer device.Close()
				defer remote.Close()
				reading := make(chan struct{}, 1)
				switch path {
				case "handoff":
					req.result <- &admissionBridgeReadConn{remote, reading}
				case "dial-rejection":
					close(req.result)
					cancel()
				case "shutdown":
					cancel()
				}
				synctest.Wait()
				select {
				case err := <-finished:
					t.Fatalf("Serve returned before socket watcher joined: %v", err)
				default:
				}
				select {
				case <-reading:
					t.Error("bridge started before socket watcher joined")
				default:
				}
				close(local.release)
				if path == "handoff" {
					<-reading
					cancel()
				}
				if err := <-finished; err != nil {
					t.Error(err)
				}
			})
		})
	}
}
