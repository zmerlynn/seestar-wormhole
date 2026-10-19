package proxy

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func TestStreamHalfCloseAndDeadlines(t *testing.T) {
	s := &Session{in: make(chan session.Event, 8), work: make(chan func(core.Instant), 8), done: make(chan struct{}), clients: make(map[uint64]*stream)}
	c := newStream(s, 1, 4700)
	var got []p2ptunnel.Event
	c.step = func(e p2ptunnel.Event, _ core.Instant) { got = append(got, e) }
	c.ready <- p2ptunnel.Action{Channel: 7, Generation: 3, Token: 9}
	if n, err := c.Write([]byte("request")); n != 7 || err != nil {
		t.Fatalf("write %d %v", n, err)
	}
	(<-s.work)(0)
	c.ready <- p2ptunnel.Action{Channel: 7, Generation: 3, Token: 10}
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	(<-s.work)(0)
	if len(got) != 2 || got[0].Kind != p2ptunnel.LocalData || string(got[0].Data) != "request" || got[1].Kind != p2ptunnel.LocalEOF || got[1].Token != 10 {
		t.Fatalf("wrong events: %+v", got)
	}
	if _, err := c.Write([]byte("late")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("write after END: %v", err)
	}
	c.rx <- delivery{p2ptunnel.Action{Channel: 7, Generation: 3, Token: 11, Data: []byte("response")}}
	b := make([]byte, 32)
	n, err := c.Read(b)
	if err != nil || string(b[:n]) != "response" {
		t.Fatalf("reverse after END: %q %v", b[:n], err)
	}
	(<-s.work)(0)
	if got[2].Kind != p2ptunnel.LocalWritten || got[2].Count != 8 {
		t.Fatal("missing consumption completion")
	}
	c.SetReadDeadline(time.Unix(1, 0))
	if _, err = c.Read(b); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	c.SetReadDeadline(time.Time{})
	close(c.readEnd)
	if _, err = c.Read(b); !errors.Is(err, io.EOF) {
		t.Fatalf("peer END: %v", err)
	}
	c.Close()
}

func TestStreamWriteDeadlinePreservesReservation(t *testing.T) {
	s := &Session{work: make(chan func(core.Instant), 1), done: make(chan struct{})}
	c := newStream(s, 1, 80)
	c.ready <- p2ptunnel.Action{Token: 42}
	c.SetWriteDeadline(time.Unix(1, 0))
	if _, err := c.Write([]byte("x")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal(err)
	}
	if a := <-c.ready; a.Token != 42 {
		t.Fatal("lost read reservation")
	}
}

func TestStreamCloseIsIdempotent(t *testing.T) {
	s := &Session{clients: make(map[uint64]*stream), in: make(chan session.Event, 8), done: make(chan struct{}), cancel: func() { t.Error("repeat Close exhausted inbox") }}
	c := newStream(s, 1, 80)
	s.clients[1] = c
	for i := 0; i < 20; i++ {
		c.Close()
	}
	if len(s.in) != 1 {
		t.Fatalf("Close emitted %d cancellations, want 1", len(s.in))
	}
}
