package proxy

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"io"
	"net"
	"testing"
)

func TestTransportFailureIsNotEOF(t *testing.T) {
	s := &Session{done: make(chan struct{})}
	c := newStream(s, 1, 4700)
	c.finish(session.ErrTransport)
	_, err := c.Read(make([]byte, 1))
	if errors.Is(err, io.EOF) {
		t.Fatalf("transport failure became clean EOF: %v", err)
	}
}

func TestTerminalReadDrainsBytes(t *testing.T) {
	for _, terminal := range []error{session.ErrTransport, io.EOF, net.ErrClosed} {
		s := &Session{done: make(chan struct{})}
		c := newStream(s, 1, 4700)
		c.rx <- delivery{action: p2ptunnel.Action{Data: []byte("abc")}}
		c.finish(terminal)
		var got []byte
		for {
			b := make([]byte, 1)
			n, err := c.Read(b)
			got = append(got, b[:n]...)
			if err != nil {
				if !errors.Is(err, terminal) {
					t.Fatalf("terminal %v, want %v", err, terminal)
				}
				break
			}
		}
		if string(got) != "abc" {
			t.Fatalf("drained %q", got)
		}
	}
}

func TestTunnelFailureIsNotEOF(t *testing.T) {
	for _, reason := range []string{"malformed tunnel frame", "receive budget exceeded", "transport failed"} {
		s := &Session{clients: make(map[uint64]*stream), in: make(chan session.Event, 1), done: make(chan struct{})}
		c := newStream(s, 1, 4700)
		s.clients[1] = c
		x := &stack{f: &factory{s: s}, streams: map[uint64]*stream{1: c}}
		x.channels[1] = c
		// Tunnel stop emits per-channel CloseLocal before SessionClosed. Its cause
		// must survive the later whole-stack failure notification.
		x.tunnelAction(p2ptunnel.Action{Kind: p2ptunnel.CloseLocal, Channel: 1, Reason: reason})
		if _, err := c.Read(make([]byte, 1)); !errors.Is(err, session.ErrTransport) {
			t.Fatalf("%s became %v", reason, err)
		}
	}
}
