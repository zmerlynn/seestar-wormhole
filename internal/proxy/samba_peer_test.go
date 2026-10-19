package proxy

// This fixture edge only bridges tunnel local actions to the read-only Samba
// fixture. Rendezvous, DTLS, RDT, tunnel and SMB all retain their real codecs.
import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
)

type sambaPeer struct {
	ctx    context.Context
	cancel context.CancelFunc
	addr   string
	events chan p2ptunnel.Event
	conns  map[byte]net.Conn // owned by the peer loop; readers borrow one connection
	wg     sync.WaitGroup
}

func newSambaPeer(ctx context.Context, addr string) *sambaPeer {
	ctx, cancel := context.WithCancel(ctx)
	return &sambaPeer{ctx: ctx, cancel: cancel, addr: addr, events: make(chan p2ptunnel.Event, 256), conns: make(map[byte]net.Conn)}
}
func (p *sambaPeer) close() {
	p.cancel()
	for _, c := range p.conns {
		c.Close()
	}
	p.wg.Wait()
}
func (p *sambaPeer) emit(e p2ptunnel.Event) {
	select {
	case p.events <- e:
	case <-p.ctx.Done():
	}
}
func (p *sambaPeer) handle(a p2ptunnel.Action, enqueue func(layerEvent)) bool {
	send := func(e p2ptunnel.Event) { enqueue(layerEvent{t: &e}) }
	switch a.Kind {
	case p2ptunnel.ConnectLocal:
		e := event(a, p2ptunnel.Connected)
		if a.Port != 445 {
			e.Failed = true
			send(e)
			return true
		}
		c, err := (&net.Dialer{Timeout: time.Second}).DialContext(p.ctx, "tcp", p.addr)
		if err != nil {
			e.Failed = true
		} else {
			p.conns[a.Channel] = c
		}
		send(e)
		if err == nil {
			send(event(a, p2ptunnel.ReadReady))
		}
	case p2ptunnel.WriteLocal:
		e := event(a, p2ptunnel.LocalWritten)
		c := p.conns[a.Channel]
		if c == nil {
			e.Kind = p2ptunnel.LocalWriteFailed
		} else {
			c.SetWriteDeadline(time.Now().Add(5 * time.Second))
			n, err := c.Write(a.Data)
			e.Count = n
			if err != nil {
				e.Kind = p2ptunnel.LocalWriteFailed
			}
		}
		send(e)
	case p2ptunnel.ReadLocal:
		c := p.conns[a.Channel]
		if c == nil {
			send(event(a, p2ptunnel.LocalEOF))
			return true
		}
		// One reader per channel, admitted only by ReadLocal's byte reservation.
		p.wg.Go(func() {
			b := make([]byte, p2ptunnel.MaxData)
			n, err := c.Read(b)
			e := event(a, p2ptunnel.LocalData)
			e.Data = b[:n]
			if n == 0 && err != nil {
				e.Kind = p2ptunnel.LocalEOF
			}
			p.emit(e)
			if n > 0 || err == nil {
				p.emit(event(a, p2ptunnel.ReadReady))
			}
		})
	case p2ptunnel.CloseLocal:
		if c := p.conns[a.Channel]; c != nil {
			c.Close()
			delete(p.conns, a.Channel)
		}
	case p2ptunnel.ShutdownWrite:
		if c, ok := p.conns[a.Channel].(interface{ CloseWrite() error }); ok {
			c.CloseWrite()
		}
	default:
		return false
	}
	return true
}
