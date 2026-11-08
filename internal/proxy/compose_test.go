package proxy

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy/proxytest"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// The simulator is proxytest's; these names keep package proxy's tests short.
type (
	simulatedCloud = proxytest.Cloud
	simulatedWire  = proxytest.Wire
)

var _ wireRunner = (*simulatedWire)(nil)

func newSimulatedWire(t *testing.T, trace *[]string) *simulatedWire {
	return proxytest.NewWire(t, proxytest.WireOptions{Trace: trace})
}

func TestComposedAllocationRPCHalfCloseRelease(t *testing.T) {

	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")

	synctest.Test(t, func(t *testing.T) {
		cloud := &simulatedCloud{}
		client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: cloud})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var trace []string
		s, err := start(ctx, Config{}, func(s *Session) sessionedge.Factory {
			return &factory{s: s, cloud: client, device: proxytest.Device, verify: proxytest.Verify, wire: func(iotc.License) wireRunner { return newSimulatedWire(t, &trace) }}
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatalf("dial: %v allocations=%d stops=%d trace=%v", err, cloud.Allocs.Load(), cloud.Stops.Load(), trace)
		}
		c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err = c.Write([]byte(rpcLine(1, 0))); err != nil {
			t.Fatal(err)
		}
		if err = c.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(c)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"state\":\"idle\"}}\n" {
			t.Fatalf("RPC response %q", b)
		}
		c.Close()
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		if cloud.Logins.Load() != 1 || cloud.Allocs.Load() != 1 || cloud.Stops.Load() != 1 {
			t.Fatalf("allocation/stop %d/%d", cloud.Allocs.Load(), cloud.Stops.Load())
		}
		if len(trace) != 1 || trace[0] != "drop-first-dtls" {
			t.Fatalf("impairment trace %v", trace)
		}
		synctest.Wait()
	})
}

func TestComposedIdleClientsLeaveSiblingWriteCredit(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		cloud := &simulatedCloud{}
		client, err := newFixtureClient(t, cloudhttp.Config{Transport: cloud})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var trace []string
		s, err := start(ctx, Config{}, func(s *Session) sessionedge.Factory {
			return &factory{s: s, cloud: client, device: proxytest.Device, verify: proxytest.Verify, wire: func(iotc.License) wireRunner { return newSimulatedWire(t, &trace) }}
		})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		for i := 0; i < 128; i++ {
			c, err := s.DialPort(ctx, 4700)
			if err != nil {
				t.Fatalf("client %d: %v", i, err)
			}
			defer c.Close()
			if i == 127 {
				c.SetDeadline(time.Now().Add(2 * time.Second))
				if _, err = c.Write([]byte("{\"id\":1,\"method\":\"get_device_state\",\"params\":{\"keys\":[\"client\",\"camera\",\"second_camera\"]}}\r\n")); err != nil {
					t.Fatalf("idle siblings exhausted write credit: %v", err)
				}
				b := make([]byte, 128)
				if n, err := c.Read(b); err != nil || !strings.Contains(string(b[:n]), `"state":"idle"`) {
					t.Fatalf("sibling response %q %v", b[:n], err)
				}
			}
		}
	})
}

func TestComposedRepeatedStartStopJoinsWorkers(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		// Check each concrete attempt; process-global goroutine counts include
		// transient test/runtime workers unrelated to this session.
		for i := 0; i < 11; i++ {
			cloud := &simulatedCloud{}
			client, err := newFixtureClient(t, cloudhttp.Config{Transport: cloud})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			var trace []string
			stacks := make(chan *stack, 1)
			s, err := start(ctx, Config{}, func(s *Session) sessionedge.Factory {
				return &trackingFactory{factory: &factory{s: s, cloud: client, device: proxytest.Device, verify: proxytest.Verify, wire: func(iotc.License) wireRunner { return newSimulatedWire(t, &trace) }}, stacks: stacks}
			})
			if err != nil {
				t.Fatal(err)
			}
			c, err := s.DialPort(ctx, 4700)
			if err != nil {
				t.Fatal(err)
			}
			x := <-stacks
			c.Close()
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			cancel()
			client.Close()
			if cloud.Allocs.Load() != 1 || cloud.Stops.Load() != 1 {
				t.Fatal("lease not released exactly once")
			}
			assertStackWorkersExited(t, x)
			assertDTLSWorkersExited(t, x)
		}
	})
}

func simulatedSession(t *testing.T, ctx context.Context, cfg Config, cloud *simulatedCloud) *Session {
	t.Helper()
	client, err := newFixtureClient(t, cloudhttp.Config{Transport: cloud})
	if err != nil {
		t.Fatal(err)
	}
	var trace []string
	s, err := start(ctx, cfg, func(s *Session) sessionedge.Factory {
		return &factory{s: s, cloud: client, device: proxytest.Device, verify: proxytest.Verify, wire: func(iotc.License) wireRunner { return newSimulatedWire(t, &trace) }}
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestComposedLazyIdleRedialAndBusy(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cloud := &simulatedCloud{}
		s := simulatedSession(t, ctx, Config{Idle: time.Second}, cloud)
		defer s.Close()
		synctest.Wait()
		if cloud.Details.Load() != 0 {
			t.Fatal("lazy startup contacted cloud")
		}
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		timer := time.NewTimer(2 * time.Second)
		<-timer.C
		synctest.Wait()
		if cloud.Stops.Load() != 1 {
			t.Fatal("idle session not released")
		}
		c, err = s.DialPort(ctx, 445)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		s.Close()
		if cloud.Allocs.Load() != 2 || cloud.Stops.Load() != 2 {
			t.Fatal("idle redial ownership")
		}

		busy := &simulatedCloud{Busy: true}
		s = simulatedSession(t, ctx, Config{}, busy)
		defer s.Close()
		// Inspect the settled refusal, rather than racing startup cleanup's
		// transport rejection before the busy backoff is installed.
		synctest.Wait()
		if _, err = s.DialPort(ctx, 80); !errors.Is(err, session.ErrBusy) {
			t.Fatalf("busy: %v", err)
		}
		timer.Reset(time.Minute)
		<-timer.C
		synctest.Wait()
		if busy.Details.Load() < 2 || busy.Allocs.Load() != 0 {
			t.Fatal("first busy did not retry without allocating")
		}
	})
}
