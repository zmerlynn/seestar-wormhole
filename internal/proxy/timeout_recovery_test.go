package proxy

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"os"
	"testing"
	"testing/synctest"
	"time"
)

func TestChangeBlockedDeadlines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &Session{work: make(chan func(core.Instant), 8), done: make(chan struct{})}
		c := newStream(s, 1, 4700)
		c.step = func(p2ptunnel.Event, core.Instant) {}
		reads := make(chan error, 1)
		go func() { _, e := c.Read(make([]byte, 1)); reads <- e }()
		synctest.Wait()
		c.SetReadDeadline(time.Now().Add(time.Second))
		if e := <-reads; !errors.Is(e, os.ErrDeadlineExceeded) {
			t.Fatal(e)
		}
		c.SetReadDeadline(time.Time{})
		c.rx <- delivery{p2ptunnel.Action{Data: []byte("x")}}
		b := make([]byte, 1)
		if n, e := c.Read(b); n != 1 || e != nil {
			t.Fatalf("read reset %d %v", n, e)
		}
		writes := make(chan error, 1)
		go func() { _, e := c.Write([]byte("x")); writes <- e }()
		synctest.Wait()
		c.SetWriteDeadline(time.Now().Add(time.Second))
		if e := <-writes; !errors.Is(e, os.ErrDeadlineExceeded) {
			t.Fatal(e)
		}
		c.SetWriteDeadline(time.Time{})
		c.ready <- p2ptunnel.Action{Token: 1}
		if n, e := c.Write([]byte("x")); n != 1 || e != nil {
			t.Fatalf("write reset %d %v", n, e)
		}
		c.finish(nil)
	})
}
