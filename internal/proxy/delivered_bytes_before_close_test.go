package proxy

import (
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"io"
	"testing"
)

func TestDeliveredBytesBeforeClose(t *testing.T) {
	for i := 0; i < 50000; i++ {
		s := &Session{done: make(chan struct{})}
		c := newStream(s, 1, 4700)
		ready := make(chan struct{})
		result := make(chan struct {
			b   []byte
			err error
		}, 1)
		go func() {
			close(ready)
			b, err := io.ReadAll(c)
			result <- struct {
				b   []byte
				err error
			}{b, err}
		}()
		<-ready
		c.rx <- delivery{action: p2ptunnel.Action{Data: []byte("abc")}}
		c.finish(io.EOF)
		r := <-result
		if string(r.b) != "abc" || r.err != nil {
			t.Fatalf("iteration %d: delivered abc before finish, ReadAll returned %q, %v", i, r.b, r.err)
		}
	}
}
