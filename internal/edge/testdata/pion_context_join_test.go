package dtls

import (
	"context"
	"net"
	"testing"
	"testing/synctest"
	"time"
)

type reviewPacket struct{}

func (reviewPacket) ReadFrom([]byte) (int, net.Addr, error)    { return 0, nil, net.ErrClosed }
func (reviewPacket) WriteTo(b []byte, _ net.Addr) (int, error) { return len(b), nil }
func (reviewPacket) Close() error                              { return nil }
func (reviewPacket) LocalAddr() net.Addr                       { return &net.UDPAddr{} }
func (reviewPacket) SetDeadline(time.Time) error               { return nil }
func (reviewPacket) SetReadDeadline(time.Time) error           { return nil }
func (reviewPacket) SetWriteDeadline(time.Time) error          { return nil }

// Gate the per-write watcher's evaluation of its parent Done channel. This is
// a scheduler barrier only; it allows the owning goroutine to cancel and close.
type reviewContext struct {
	context.Context
	entered, release, exited chan struct{}
}

func (c *reviewContext) Done() <-chan struct{} {
	close(c.entered)
	<-c.release
	close(c.exited)
	return nil
}
func TestAdversarialWriteContextWatcherJoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, err := ClientWithOptions(reviewPacket{}, &net.UDPAddr{}, WithCipherSuites(TLS_PSK_WITH_AES_128_GCM_SHA256), WithPSKIdentityHint([]byte("id")), WithPSK(func([]byte) ([]byte, error) { return make([]byte, 32), nil }))
		if err != nil {
			t.Fatal(err)
		}
		ctx := &reviewContext{context.Background(), make(chan struct{}), make(chan struct{}), make(chan struct{})}
		_, cancel := c.contextWithClose(ctx)
		<-ctx.entered
		closed := make(chan struct{})
		go func() { cancel(); c.Close(); close(closed) }()
		synctest.Wait()
		early := false
		select {
		case <-closed:
			early = true
		default:
		}
		close(ctx.release)
		<-ctx.exited
		<-closed
		if early {
			t.Fatal("Conn.Close returned with its per-Write contextWithClose goroutine still running")
		}
	})
}
