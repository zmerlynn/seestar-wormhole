package main

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"net"
)

type idleCommandSession struct{ done chan struct{} }

func (s *idleCommandSession) Close() error             { close(s.done); return nil }
func (s *idleCommandSession) Done() <-chan struct{}    { return s.done }
func (*idleCommandSession) DeviceDetail() cloud.Detail { return cloud.Detail{} }
func (*idleCommandSession) DialPort(ctx context.Context, _ uint16) (net.Conn, error) {
	return nil, errors.New("fixture has no device connection")
}
func idleCommandFixture(context.Context, proxy.Config) (commandSession, error) {
	return &idleCommandSession{make(chan struct{})}, nil
}
