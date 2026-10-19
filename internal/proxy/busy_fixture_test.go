package proxy

import (
	"context"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
	"sync/atomic"
)

type busyFactory struct {
	s         *Session
	allocate  func(context.Context) (sessionedge.Lease, error)
	channels  chan *busyChannels
	stops     atomic.Int32
	closed    atomic.Int32
	stopError error
}

func (f *busyFactory) New(uint64, session.Budgets) (sessionedge.Layers, error) {
	a := &busyChannels{f: f, events: make(chan session.Event, 1)}
	f.channels <- a
	return sessionedge.Layers{Cloud: f, Rendezvous: a, DTLS: a, RDT: a, Tunnel: a}, nil
}
func (f *busyFactory) Allocate(ctx context.Context) (sessionedge.Lease, error) {
	return f.allocate(ctx)
}
func (f *busyFactory) Stop(context.Context) error {
	f.stops.Add(1)
	return f.stopError
}

type busyTransport struct{ f *busyFactory }

func (a *busyTransport) Close() error               { a.f.closed.Add(1); return nil }
func (*busyTransport) Events() <-chan session.Event { return nil }

type busyChannels struct {
	f      *busyFactory
	events chan session.Event
}

func (a *busyChannels) Dial(context.Context, sessionedge.Lease) (sessionedge.Transport, error) {
	return &busyTransport{a.f}, nil
}
func (a *busyChannels) Handshake(context.Context, sessionedge.Transport) (sessionedge.Transport, error) {
	return &busyTransport{a.f}, nil
}
func (a *busyChannels) Create(context.Context, sessionedge.Transport) (sessionedge.Transport, error) {
	return &busyTransport{a.f}, nil
}
func (a *busyChannels) Start(context.Context, sessionedge.Transport) (sessionedge.Channels, error) {
	return a, nil
}
func (a *busyChannels) Events() <-chan session.Event { return a.events }
func (a *busyChannels) Close() error                 { a.f.closed.Add(1); return nil }
func (*busyChannels) TryClose(uint64)                {}
func (a *busyChannels) TryOpen(id uint64) error {
	a.f.s.mu.Lock()
	defer a.f.s.mu.Unlock()
	a.f.s.clients[id].result <- nil
	return nil
}
