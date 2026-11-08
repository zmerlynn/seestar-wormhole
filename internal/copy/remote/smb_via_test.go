package remote

// These tests run DialVia's go-smb2 client against the scripted peer of
// smb_wire_test.go, over a stream a Conn supplies instead of a TCP dial.

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hirochachacha/go-smb2/v2/x/wire"
)

// viaName is the display name the tests give DialVia; nothing dials it.
const viaName = "Telescope (test route)"

// closeSpy counts the closes of the stream a Conn supplied.
type closeSpy struct {
	net.Conn
	closes atomic.Int32
}

func (c *closeSpy) Close() error {
	c.closes.Add(1)
	return c.Conn.Close()
}

// viaConn supplies the peer's client end once, behind a close spy. It
// counts its calls.
func (p *peer) viaConn() (Conn, *closeSpy, *atomic.Int32) {
	spy := &closeSpy{Conn: p.client}
	var calls atomic.Int32
	return func(ctx context.Context) (net.Conn, error) {
		calls.Add(1)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return spy, nil
	}, spy, &calls
}

// dialVia runs DialVia's configuration over conn, with the peer's test
// credentials in place of NTLM.
func (p *peer) dialVia(ctx context.Context, conn Conn) (*SMB, error) {
	d := newDialer()
	d.Credentials = testCredentials{}
	return dialVia(ctx, viaName, conn, d, p.log)
}

// TestDialViaUsesSuppliedConn checks that negotiate, the guest session and
// the mount run over the supplied stream, and that the share path names
// the fixed loopback server rather than anything the caller passed.
func TestDialViaUsesSuppliedConn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := startPeer(t, &peer{file: `MyWorks\a.fit`, data: []byte("frame")})
		defer p.stop()
		conn, spy, calls := p.viaConn()
		done := make(chan error, 1)
		var s *SMB
		go func() {
			var err error
			s, err = p.dialVia(t.Context(), conn)
			done <- err
		}()
		if err := await(t, done, time.Second, "DialVia"); err != nil {
			t.Fatalf("DialVia: %v", err)
		}
		if n := calls.Load(); n != 1 {
			t.Errorf("Conn called %d times, want 1", n)
		}
		var cmds []wire.Command
		var path string
		for _, req := range p.seen() {
			cmds = append(cmds, req.Command())
			if req.Command() == wire.SMB2_TREE_CONNECT {
				path = wire.TreeConnectRequestDecoder(req.Body()).Path()
			}
		}
		want := []wire.Command{wire.SMB2_NEGOTIATE, wire.SMB2_SESSION_SETUP, wire.SMB2_TREE_CONNECT}
		if len(cmds) != len(want) {
			t.Fatalf("requests = %v, want %v", cmds, want)
		}
		for i := range want {
			if cmds[i] != want[i] {
				t.Fatalf("requests = %v, want %v", cmds, want)
			}
		}
		if wantPath := `\\127.0.0.1\` + ShareName; path != wantPath {
			t.Errorf("TREE_CONNECT path = %q, want %q", path, wantPath)
		}
		if _, err := s.ReadDir(t.Context(), "Missing"); err == nil || !strings.Contains(err.Error(), viaName) {
			t.Errorf("ReadDir error = %v, want one naming %q", err, viaName)
		}
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		if n := spy.closes.Load(); n != 1 {
			t.Errorf("stream closed %d times after Close, want 1", n)
		}
	})
}

// TestDialViaCancelAtEachStage cancels DialVia at each stage of the
// connection and checks that it returns at once with the cancellation and
// that the supplied stream is closed exactly once.
func TestDialViaCancelAtEachStage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hold  []wire.Command // the request the peer holds, if any
		admit bool           // the Conn blocks until ctx ends
		after bool           // cancel after DialVia succeeded
	}{
		{name: "admission", admit: true},
		{name: "negotiate", hold: []wire.Command{wire.SMB2_NEGOTIATE}},
		{name: "session setup", hold: []wire.Command{wire.SMB2_SESSION_SETUP}},
		{name: "tree connect", hold: []wire.Command{wire.SMB2_TREE_CONNECT}},
		{name: "after mount", after: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := startPeer(t, &peer{file: `MyWorks\a.fit`, data: []byte("frame"), hold: tc.hold})
				defer p.stop()
				conn, spy, _ := p.viaConn()
				entered := make(chan struct{})
				if tc.admit {
					conn = func(ctx context.Context) (net.Conn, error) {
						close(entered)
						<-ctx.Done()
						return nil, ctx.Err()
					}
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				var s *SMB
				go func() {
					var err error
					s, err = p.dialVia(ctx, conn)
					done <- err
				}()
				switch {
				case tc.admit:
					<-entered
				case tc.hold != nil:
					p.awaitHeld(t)
				default:
					if err := await(t, done, time.Second, "DialVia"); err != nil {
						t.Fatalf("DialVia: %v", err)
					}
				}
				start := time.Now()
				cancel()
				if tc.after {
					synctest.Wait()
					if _, err := s.ReadDir(t.Context(), "MyWorks"); err == nil {
						t.Error("ReadDir after the cancel succeeded")
					}
				} else {
					err := await(t, done, time.Second, "DialVia")
					if !errors.Is(err, context.Canceled) {
						t.Errorf("DialVia = %v, want context.Canceled", err)
					}
				}
				if d := time.Since(start); d != 0 {
					t.Errorf("returned %v after the cancel, want no wait", d)
				}
				synctest.Wait()
				want := int32(1)
				if tc.admit {
					want = 0 // no stream was ever supplied
				}
				if n := spy.closes.Load(); n != want {
					t.Errorf("stream closed %d times, want %d", n, want)
				}
			})
		})
	}
}

// TestDialViaPeerErrorClosesConn has the peer refuse NEGOTIATE.
func TestDialViaPeerErrorClosesConn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := startPeer(t, &peer{negotiate: statusNotSupported})
		defer p.stop()
		conn, spy, _ := p.viaConn()
		done := make(chan error, 1)
		go func() {
			s, err := p.dialVia(t.Context(), conn)
			if err == nil {
				_ = s.Close()
			}
			done <- err
		}()
		err := await(t, done, time.Second, "DialVia")
		if err == nil || !strings.Contains(err.Error(), viaName) {
			t.Errorf("DialVia = %v, want an error naming %q", err, viaName)
		}
		synctest.Wait()
		if n := spy.closes.Load(); n != 1 {
			t.Errorf("stream closed %d times, want 1", n)
		}
	})
}
