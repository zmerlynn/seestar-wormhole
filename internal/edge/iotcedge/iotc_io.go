package iotcedge

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
)

// IOTCIO is the production socket/DNS/crypto executor. Run owns all resources;
// inputs and emitted completions are bounded by its caller. It never calls Step.
// Every child joins before Run returns. One instance serves one attempt only.
type IOTCIO struct {
	Mask [16]byte
	// Logger is the caller-owned sink; nil disables diagnostics. Its handler
	// must not block, just as for the enclosing session owner.
	Logger *slog.Logger
}

type tcpPath struct {
	cancel context.CancelFunc
	writes chan iotc.Action
}

// Run executes effects until cancellation. emit must honor ctx and return false
// after cancellation; it may apply backpressure to this I/O worker, never an owner.
func (x *IOTCIO) Run(ctx context.Context, actions <-chan iotc.Action, emit func(iotc.Event) bool) {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	crypt := IOTCCrypto{}
	paths := make(map[iotc.Link]*tcpPath)
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		emit(iotc.Event{Kind: iotc.AttemptFailed})
		cancel()
		return
	}
	defer func() {
		cancel()
		udp.Close()
		for _, p := range paths {
			p.cancel()
		}
		wg.Wait()
		crypt.Clear()
	}()
	wg.Go(func() { <-ctx.Done(); udp.Close() })
	wg.Go(func() {
		b := make([]byte, iotc.MaxPacket+1)
		for {
			n, addr, err := udp.ReadFromUDPAddrPort(b)
			if err != nil {
				return
			}
			if !emit(iotc.Event{Kind: iotc.Wire, Link: iotc.Link{Address: addr}, Data: append([]byte(nil), b[:n]...), Marker: marker()}) {
				return
			}
		}
	})
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-actions:
			e := iotc.Event{Attempt: a.Attempt, Token: a.Token, Link: a.Link}
			switch a.Kind {
			case iotc.Resolve:
				// At most one Resolve is issued by the machine per attempt.
				wg.Go(func() {
					addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", a.Domain)
					e.Kind = iotc.Resolved
					if err != nil {
						e.Kind = iotc.EffectFailed
					} else {
						e.Addresses = append([]netip.Addr(nil), addresses[:min(4, len(addresses))]...)
					}
					emit(e)
				})
			case iotc.Generate, iotc.Derive, iotc.Seal, iotc.Open:
				var err error
				e, err = crypt.Execute(a)
				if err != nil {
					var exhausted *RIDHistoryExhaustedError
					if errors.As(err, &exhausted) && x.Logger != nil {
						x.Logger.Error("iotc_rid_history_exhausted", "error", exhausted)
					}
					e = iotc.Event{Kind: iotc.EffectFailed, Attempt: a.Attempt, Token: a.Token, Link: a.Link}
				}
				if !emit(e) {
					return
				}
			case iotc.OpenTCP:
				if len(paths) >= 32 {
					e.Kind = iotc.LinkFailed
					emit(e)
					continue
				}
				if old := paths[a.Link]; old != nil {
					old.cancel()
				}
				pc, stop := context.WithCancel(ctx)
				p := &tcpPath{stop, make(chan iotc.Action, 32)}
				paths[a.Link] = p
				wg.Go(func() { x.tcp(pc, a.Link, p.writes, emit) })
			case iotc.CloseLink:
				if p := paths[a.Link]; p != nil {
					p.cancel()
					delete(paths, a.Link)
				}
			case iotc.Send:
				if a.Link.Transport == iotc.UDP {
					udp.SetWriteDeadline(time.Now().Add(time.Second))
					_, err := udp.WriteToUDPAddrPort(a.Data, a.Link.Address)
					if err != nil {
						e.Kind = iotc.LinkFailed
					} else {
						e.Kind = iotc.Sent
					}
					if (e.Kind != iotc.Sent || e.Token != 0) && !emit(e) {
						return
					}
				} else if p := paths[a.Link]; p != nil {
					select {
					case p.writes <- a:
					case <-ctx.Done():
						return
					}
				} else {
					e.Kind = iotc.LinkFailed
					emit(e)
				}
			case iotc.Disposed:
				return
			}
		}
	}
}
func marker() iotc.Marker {
	n := time.Now()
	return iotc.Marker{Seconds: n.Unix(), Micros: int32(n.Nanosecond() / 1000)}
}
func (x *IOTCIO) tcp(ctx context.Context, link iotc.Link, writes <-chan iotc.Action, emit func(iotc.Event) bool) {
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp4", link.Address.String())
	if err != nil {
		emit(iotc.Event{Kind: iotc.LinkFailed, Link: link})
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { cancel(); c.Close(); wg.Wait() }()
	wg.Go(func() { <-ctx.Done(); c.Close() })
	wg.Go(func() {
		defer cancel()
		var d iotc.Decoder
		b := make([]byte, 8192)
		for {
			n, err := c.Read(b)
			if n > 0 {
				packets, e := d.Feed(b[:n], iotc.ScrambleKey(x.Mask))
				if e != nil {
					break
				}
				for _, packet := range packets {
					if !emit(iotc.Event{Kind: iotc.Wire, Link: link, Data: packet, Marker: marker()}) {
						return
					}
				}
			}
			if err != nil {
				break
			}
		}
		emit(iotc.Event{Kind: iotc.LinkFailed, Link: link})
	})
	if !emit(iotc.Event{Kind: iotc.Connected, Link: link}) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-writes:
			b := a.Data
			for len(b) > 0 {
				n, e := c.Write(b)
				b = b[n:]
				if e != nil || n == 0 {
					emit(iotc.Event{Kind: iotc.LinkFailed, Link: link})
					return
				}
			}
			if a.Token != 0 && !emit(iotc.Event{Kind: iotc.Sent, Attempt: a.Attempt, Token: a.Token, Link: link}) {
				return
			}
		}
	}
}
