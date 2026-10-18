package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

type cleanupSession struct {
	cancel    context.CancelFunc
	done      chan struct{}
	allocated chan struct{}
	stops     atomic.Int32
	closes    atomic.Int32
	result    error
	detail    cloud.Detail
	stopErr   error
}

func (s *cleanupSession) Allocate(context.Context) (sessionedge.Lease, error) {
	close(s.allocated)
	return s, nil
}
func (s *cleanupSession) Stop(context.Context) error { s.stops.Add(1); return s.stopErr }
func (s *cleanupSession) Dial(ctx context.Context, _ sessionedge.Lease) (sessionedge.Transport, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (s *cleanupSession) Handshake(context.Context, sessionedge.Transport) (sessionedge.Transport, error) {
	panic("unused")
}
func (s *cleanupSession) Create(context.Context, sessionedge.Transport) (sessionedge.Transport, error) {
	panic("unused")
}
func (s *cleanupSession) Start(context.Context, sessionedge.Transport) (sessionedge.Channels, error) {
	panic("unused")
}
func (s *cleanupSession) New(uint64, session.Budgets) (sessionedge.Layers, error) {
	return sessionedge.Layers{Cloud: s, Rendezvous: s, DTLS: s, RDT: s, Tunnel: s}, nil
}
func (s *cleanupSession) Close() error {
	s.closes.Add(1)
	s.cancel()
	<-s.done
	if errors.Is(s.result, context.Canceled) && !errors.Is(s.result, session.ErrStop) {
		return nil
	}
	return s.result
}
func (s *cleanupSession) Done() <-chan struct{}      { return s.done }
func (s *cleanupSession) DeviceDetail() cloud.Detail { <-s.allocated; return s.detail }
func (s *cleanupSession) DialPort(ctx context.Context, _ uint16) (net.Conn, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, session.ErrClosed
	}
}

type cleanupRNG struct{}

func (cleanupRNG) Uint64() uint64 { return 0 }

// Production discovery requires an active broadcast IPv4 interface. These
// cleanup schedules use that listener, so loopback-only containers explicitly
// skip the discovery cases; context and owner cleanup still run there.
func requireDiscoveryInterface(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("LAN discovery is unsupported on Windows")
	}
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagBroadcast == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			t.Fatal(err)
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err == nil && prefix.Addr().Is4() && prefix.Addr().IsGlobalUnicast() {
				return
			}
		}
	}
	t.Skip("discovery cleanup needs an active non-loopback broadcast IPv4 interface; loopback-only network")
}

func TestEarlyDiscoveryFailureRetainsFinalCleanup(t *testing.T) {
	t.Run("acquired", func(t *testing.T) {
		requireDiscoveryInterface(t)
		var logs bytes.Buffer
		var s *cleanupSession
		newSession := func(ctx context.Context, _ proxy.Config) (commandSession, error) {
			detail := cloud.Detail{}
			ctx, cancel := context.WithCancel(ctx)
			s = &cleanupSession{cancel: cancel, done: make(chan struct{}), allocated: make(chan struct{}), detail: detail, stopErr: session.ErrStop}
			go func() {
				s.result = sessionedge.Run(ctx, session.DefaultConfig(), cleanupRNG{}, s, nil, make(sessionedge.ChannelSink, 16))
				close(s.done)
			}()
			return s, nil
		}
		err := runWithSession(context.Background(), append([]string{"proxy"}, []string{"--bind", "0.0.0.0", "--port", "14773:445", "--lan-discovery"}...), io.Discard, &logs, newSession)
		if s == nil {
			t.Fatalf("session fixture was not constructed: %v", err)
		}
		if !errors.Is(err, session.ErrStop) {
			t.Errorf("early exit error=%v, want final release failure", err)
		}
		if s.stops.Load() != 1 {
			t.Fatalf("stop calls=%v, want acquired lease stopped once", s)
		}
		if s.closes.Load() != 1 {
			t.Errorf("session close calls=%d, want 1", s.closes.Load())
		}
		if strings.Count(logs.String(), `"level":"ERROR"`) != 1 {
			t.Errorf("error report count: %s", &logs)
		}
		if !strings.Contains(logs.String(), `"cleanup_reason":"release_failed"`) {
			t.Errorf("final cleanup status missing: %s", &logs)
		}
		if strings.Contains(logs.String(), "secret-identity") {
			t.Fatal("template identity or path logged")
		}
	})
}

func TestCommandExitJoinsCleanupOnce(t *testing.T) {
	for _, mode := range []string{"context", "owner", "discovery"} {
		for _, failedStop := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failed-stop-%v", mode, failedStop), func(t *testing.T) {
				if mode == "discovery" {
					requireDiscoveryInterface(t)
				}
				var logs bytes.Buffer
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var s *cleanupSession
				newSession := func(parent context.Context, _ proxy.Config) (commandSession, error) {
					detail, err := cloud.ParseLANDetail([]byte(`{"code":200,"data":{"deviceSn":"selected-secret-identity","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"Seestar"}}`))
					if err != nil {
						t.Fatal(err)
					}
					ownerCtx, stop := context.WithCancel(parent)
					s = &cleanupSession{cancel: stop, done: make(chan struct{}), allocated: make(chan struct{}), detail: detail}
					if failedStop {
						s.stopErr = session.ErrStop
					}
					go func() {
						s.result = sessionedge.Run(ownerCtx, session.DefaultConfig(), cleanupRNG{}, s, nil, make(sessionedge.ChannelSink, 16))
						close(s.done)
					}()
					<-s.allocated
					if mode == "owner" {
						s.cancel()
						<-s.done
					} else {
						cancel()
					}
					return s, nil
				}
				args := []string{"--bind", "0.0.0.0", "--port", "14774:445"}
				if mode == "discovery" {
					args = append(args, "--lan-discovery")
				}
				err := runWithSession(ctx, append([]string{"proxy"}, args...), io.Discard, &logs, newSession)
				if s == nil {
					t.Fatalf("session fixture was not constructed: %v", err)
				}
				if errors.Is(err, session.ErrStop) != failedStop {
					t.Errorf("mode=%s failedStop=%v: result=%v", mode, failedStop, err)
				}
				if !failedStop && err != nil {
					t.Errorf("successful cleanup returned %v", err)
				}
				if s.stops.Load() != 1 || s.closes.Load() != 1 {
					t.Errorf("mode=%s: stops/closes=%d/%d, want 1/1", mode, s.stops.Load(), s.closes.Load())
				}
				wantReports := 0
				wantCleanup := "none"
				if failedStop {
					wantReports = 1
					wantCleanup = "release_failed"
				}
				if strings.Count(logs.String(), `"level":"ERROR"`) != wantReports {
					t.Errorf("mode=%s: error reports: %s", mode, &logs)
				}
				if !strings.Contains(logs.String(), `"msg":"proxy_cleanup_complete","cleanup_reason":"`+wantCleanup+`"`) {
					t.Errorf("mode=%s: final cleanup status missing: %s", mode, &logs)
				}
			})
		}
	}
}
