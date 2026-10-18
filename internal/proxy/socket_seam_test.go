package proxy

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/edge/iotcedge"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
)

// The RSA/rendezvous peer remains synthetic. Each outgoing envelope crosses the
// production IOTCIO writer, loopback peer and production reader/decoder before
// reaching it. TCP echo deliberately splits frames at arbitrary byte boundaries.
func socketTransport(t *testing.T, ctx context.Context, tcp bool) (func(context.Context, []byte) ([]byte, error), func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	actions := make(chan iotc.Action, 1)
	events := make(chan iotc.Event, 8)
	var wg sync.WaitGroup
	wg.Go(func() {
		(&iotcedge.IOTCIO{}).Run(ctx, actions, func(e iotc.Event) bool {
			select {
			case events <- e:
				return true
			case <-ctx.Done():
				return false
			}
		})
	})
	actions <- iotc.Action{Kind: iotc.Resolve, Domain: "localhost", Token: 7}
	e := <-events
	if e.Kind != iotc.Resolved || len(e.Addresses) == 0 || !e.Addresses[0].IsLoopback() {
		t.Fatal("production resolver did not resolve loopback")
	}
	var closePeer func()
	link := iotc.Link{}
	if tcp {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		closePeer = func() { l.Close() }
		link = iotc.Link{Transport: iotc.TCP, Address: netip.MustParseAddrPort(l.Addr().String()), ID: 4}
		wg.Go(func() {
			c, err := l.Accept()
			if err != nil {
				return
			}
			defer c.Close()
			var watcher sync.WaitGroup
			watcher.Go(func() { <-ctx.Done(); c.Close() })
			defer watcher.Wait()
			b := make([]byte, 8192)
			for {
				n, err := c.Read(b)
				if err != nil {
					return
				}
				for offset := 0; offset < n; {
					end := min(n, offset+7)
					m, e := c.Write(b[offset:end])
					if e != nil {
						return
					}
					offset += m
				}
			}
		})
		actions <- iotc.Action{Kind: iotc.OpenTCP, Link: link}
		if e := <-events; e.Kind != iotc.Connected {
			t.Fatal("production TCP connect failed")
		}
	} else {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		closePeer = func() { c.Close() }
		link.Address = c.LocalAddr().(*net.UDPAddr).AddrPort()
		wg.Go(func() {
			b := make([]byte, 8192)
			for {
				n, addr, err := c.ReadFromUDPAddrPort(b)
				if err != nil {
					return
				}
				if _, err = c.WriteToUDPAddrPort(b[:n], addr); err != nil {
					return
				}
			}
		})
	}
	var token uint64 = 100
	roundTrip := func(ctx context.Context, b []byte) ([]byte, error) {
		token++
		select {
		case actions <- iotc.Action{Kind: iotc.Send, Link: link, Token: token, Data: b}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		var received []byte
		sent := false
		for !sent || received == nil {
			select {
			case e := <-events:
				switch e.Kind {
				case iotc.Sent:
					sent = e.Token == token
				case iotc.Wire:
					received = e.Data
				case iotc.LinkFailed:
					return nil, errors.New("loopback transport failed")
				}
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if !bytes.Equal(received, b) {
			return nil, errors.New("production framing changed envelope")
		}
		return received, nil
	}
	return roundTrip, func() { cancel(); closePeer(); wg.Wait() }
}
func TestComposedProductionSocketFraming(t *testing.T) {

	for _, tcp := range []bool{false, true} {
		name := "udp"
		if tcp {
			name = "tcp"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			transport, closeTransport := socketTransport(t, ctx, tcp)
			defer closeTransport()
			s, _, _ := workloadSession(t, ctx, func(p *simulatedWire) { p.transport = transport })
			defer s.Close()
			c, err := s.DialPort(ctx, 4700)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			for i := 0; i < 4; i++ {
				if err := exchange(c.(*stream), i+1, 7900); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
