package proxy

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/dtls"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

type workloadFactory struct {
	*factory
	stacks chan *stack
}

func (f *workloadFactory) New(id uint64, b session.Budgets) (sessionedge.Layers, error) {
	l, e := f.factory.New(id, b)
	if e == nil {
		f.stacks <- l.Rendezvous.(*stack)
	}
	return l, e
}
func workloadSession(t *testing.T, ctx context.Context, configure func(*simulatedWire), policy ...Config) (*Session, chan *stack, *simulatedCloud) {
	t.Helper()
	cfg := Config{}
	if len(policy) > 0 {
		cfg = policy[0]
	}
	return workloadSessionWithTunnel(t, ctx, configure, cfg, nil)
}

// Down-state clients close immediately; recovery continues without demand.
// Poll virtual time as a reconnecting local client would, within ctx's bound.
func dialAfterRecovery(ctx context.Context, s *Session) (net.Conn, error) {
	for {
		c, err := s.DialPort(ctx, 4700)
		if !errors.Is(err, session.ErrTransport) && !errors.Is(err, session.ErrBusy) {
			return c, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func workloadSessionLegacyCapacity(t *testing.T, ctx context.Context, configure func(*simulatedWire)) (*Session, chan *stack, *simulatedCloud) {
	cfg := runtimeTunnelConfig(64 << 10)
	cfg.ChannelBytes, cfg.SessionBytes = 6<<20, 8<<20
	return workloadSessionWithTunnel(t, ctx, configure, Config{}, &cfg)
}

func workloadSessionWithTunnel(t *testing.T, ctx context.Context, configure func(*simulatedWire), cfg Config, tunnel *p2ptunnel.Config) (*Session, chan *stack, *simulatedCloud) {
	t.Helper()
	cloud := &simulatedCloud{}
	client, err := newFixtureClient(t, cloudhttp.Config{Transport: cloud})
	if err != nil {
		t.Fatal(err)
	}
	stacks := make(chan *stack, 16)
	s, err := start(ctx, cfg, func(s *Session) sessionedge.Factory {
		if tunnel != nil {
			s.tunnelConfig = *tunnel
		}
		return &workloadFactory{&factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
			trace := []string{}
			p := newSimulatedWire(t, &trace)
			configure(p)
			return p
		}}, stacks}
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, stacks, cloud
}
func rpcLine(id int, padding int) string {
	return fmt.Sprintf("{\"id\":%d,\"method\":\"get_device_state\",\"params\":{\"keys\":[\"client\",\"camera\",\"second_camera\"]},\"padding\":\"%s\"}\r\n", id, strings.Repeat("x", padding))
}
func exchange(c *stream, id, padding int) error {
	c.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := c.Write([]byte(rpcLine(id, padding))); err != nil {
		return err
	}
	b, err := bufio.NewReader(c).ReadString('\n')
	if err == nil {
		return checkResponse(b, id)
	}
	return err
}
func checkResponse(b string, id int) error {
	var reply struct {
		ID     *int `json:"id"`
		Result struct {
			State string `json:"state"`
		} `json:"result"`
		Error jsontext.Value `json:"error"`
	}
	if err := json.Unmarshal([]byte(b), &reply, json.MatchCaseInsensitiveNames(true)); err != nil {
		return fmt.Errorf("invalid response: %w", err)
	}
	if reply.ID == nil || *reply.ID != id {
		return fmt.Errorf("wrong response ID, want %d: %q", id, b)
	}
	if reply.Result.State != "idle" || len(reply.Error) > 0 {
		return fmt.Errorf("wrong response content: %q", b)
	}
	return nil
}

func inspectStack(t *testing.T, x *stack) {
	t.Helper()
	done := make(chan string, 1)
	if !x.post(func(core.Instant) {
		if x.failed {
			done <- "session failed under ordinary backpressure"
			return
		}
		owners := 0
		for id, c := range x.channels {
			if c != nil {
				owners++
				if x.streams[c.id] != c || !x.tunnel.Channel(byte(id)).Active {
					done <- "channel without one registered owner"
					return
				}
			}
		}
		if owners != x.tunnel.Usage().Channels {
			done <- "unowned opened channel"
			return
		}
		u := x.reliable.Usage()
		v := x.tunnel.Usage()
		if u.TXPackets > 256 || u.RXPackets > 8192 || u.TXBytes > 1<<20 || u.RXBytes > 8<<20 || v.Channels > 128 || len(x.pending) > 128 || len(x.effects) > 1024 || len(x.queue) > 512 {
			done <- fmt.Sprintf("budget overflow: RDT %+v tunnel %+v", u, v)
			return
		}
		done <- ""
	}) {
		t.Fatal("inspection canceled")
	}
	if err := <-done; err != "" {
		t.Fatal(err)
	}
}

func TestComposedSustainedBurstAndSlowConsumer(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		s, stacks, cloud := workloadSession(t, ctx, func(p *simulatedWire) { p.responseBytes = 96 << 10; p.delay = time.Millisecond })
		defer s.Close()
		slow, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer slow.Close()
		x := <-stacks
		slow.Write([]byte(rpcLine(99, 0)))
		time.Sleep(2 * time.Second)
		synctest.Wait()
		inspectStack(t, x)
		clients := make([]*stream, 32)
		for i := range clients {
			c, e := s.DialPort(ctx, 4700)
			if e != nil {
				t.Fatal(e)
			}
			clients[i] = c.(*stream)
			defer c.Close()
		}
		results := make(chan error, len(clients))
		for _, c := range clients {
			go func() {
				for n := 0; n < 12; n++ {
					if e := exchange(c, n+1, 7900); e != nil {
						results <- e
						return
					}
				}
				results <- nil
			}()
		}
		for range clients {
			if e := <-results; e != nil {
				t.Fatal(e)
			}
		}
		inspectStack(t, x)
		slow.SetReadDeadline(time.Now().Add(30 * time.Second))
		b, e := bufio.NewReader(slow).ReadString('\n')
		if e != nil || len(b) < 96<<10 {
			t.Fatalf("slow transfer %d bytes: %v", len(b), e)
		}
		if cloud.allocs.Load() != 1 {
			t.Fatal("backpressure replaced allocation")
		}
	})
}

// Seeds choose workloads, cancellation points and impairment periods. Barriers
// replay operation order; crypto randomness and OS scheduling are not replayed.
func TestComposedSeededInvariants(t *testing.T) {

	for _, seed := range []uint64{1, 7, 42} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rng := rand.New(rand.NewPCG(seed, seed^0x5eed))
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				suite := dtls.PSKAES128GCM
				if seed%2 == 1 {
					suite = dtls.CCAC
				}
				s, stacks, cloud := workloadSession(t, ctx, func(p *simulatedWire) {
					p.suite = suite
					p.verify = seed != 7
					p.impairEvery = 17 + int(seed%7)
					p.responseBytes = 96 << 10
				})
				defer s.Close()
				c, err := s.DialPort(ctx, 4700)
				if err != nil {
					t.Fatal(err)
				}
				c.Close()
				x := <-stacks
				if got := x.secure.Profile().Suite; got != suite {
					t.Fatalf("suite %x want %x", got, suite)
				}
				for round := 0; round < 36; round++ {
					t.Logf("seed=%d round=%d", seed, round)
					if round%12 == 11 {
						c, err := s.DialPort(ctx, 4700)
						if err != nil {
							t.Fatal(err)
						}
						x.post(func(now core.Instant) {
							x.drive(layerEvent{i: &iotc.Event{Kind: iotc.AttemptFailed, Attempt: x.id}}, now)
						})
						_, err = io.ReadAll(c)
						if !errors.Is(err, session.ErrTransport) {
							t.Fatalf("transport failure reported success: %v", err)
						}
						c.Close()
						synctest.Wait()
						time.Sleep(2 * time.Second)
						c, err = dialAfterRecovery(ctx, s)
						if err != nil {
							t.Fatal(err)
						}
						c.Close()
						assertDTLSWorkersExited(t, x)
						x = <-stacks
					} else if round%6 == 0 {
						live, err := s.DialPort(ctx, 4700)
						if err != nil {
							t.Fatal(err)
						}
						if err = canceledBurst(s, ctx, 256+rng.IntN(768)); err != nil {
							t.Fatal(err)
						}
						if err = exchange(live.(*stream), round+700, 0); err != nil {
							t.Fatalf("cancellation harmed live stream: %v", err)
						}
						live.Close()
					} else if round%6 == 1 {
						if err := rejectedSetup(s, ctx, seed != 7); err != nil {
							t.Fatal(err)
						}
					} else if round%6 == 2 {
						count := 1 + rng.IntN(4)
						results := make(chan error, count)
						for n := 0; n < count; n++ {
							mode := []string{"end", "close", "fail"}[(round/6+n)%3]
							chunk, delay := 1+rng.IntN(17), time.Duration(rng.IntN(2))*time.Millisecond
							go func() {
								c, err := s.DialPort(ctx, 4700)
								if err == nil {
									defer c.Close()
									err = endingExchange(c.(*stream), 1000+round*4+n, mode, chunk, delay)
								}
								results <- err
							}()
						}
						for range count {
							if err := <-results; err != nil {
								t.Fatal(err)
							}
						}
					} else {
						count := 1 + rng.IntN(4)
						results := make(chan error, count)
						for n := 0; n < count; n++ {
							kind, padding := rng.IntN(4), rng.IntN(10000)
							go func() {
								dial, stop := context.WithCancel(ctx)
								if kind == 0 {
									stop()
								}
								c, err := s.DialPort(dial, 4700)
								stop()
								if kind == 0 {
									if c != nil {
										c.Close()
									}
									if !errors.Is(err, context.Canceled) {
										results <- fmt.Errorf("canceled admission: %v", err)
									} else {
										results <- nil
									}
									return
								}
								if err != nil {
									results <- err
									return
								}
								defer c.Close()
								if kind == 1 {
									c.Write([]byte(rpcLine(99, padding)))
									time.Sleep(time.Second)
									c.SetReadDeadline(time.Now().Add(30 * time.Second))
									var line string
									line, err = bufio.NewReader(c).ReadString('\n')
									if err == nil {
										err = checkResponse(line, 99)
									}
								} else {
									err = exchange(c.(*stream), round+100, padding)
								}
								results <- err
							}()
						}
						for range count {
							if err := <-results; err != nil {
								t.Fatal(err)
							}
						}
					}
					synctest.Wait()
					inspectStack(t, x)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				assertDTLSWorkersExited(t, x)
				if cloud.allocs.Load() != cloud.stops.Load() {
					t.Fatal("allocation not released exactly once")
				}
			})
		})
	}
}

func TestComposedEightHourPlateau(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s, stacks, cloud := workloadSession(t, ctx, func(p *simulatedWire) {})
		defer s.Close()
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		x := <-stacks
		var base runtime.MemStats
		for minute := 0; minute <= 480; minute++ {
			if err := exchange(c.(*stream), minute+1, 7900); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			inspectStack(t, x)
			if minute%60 == 0 {
				runtime.GC()
			}
			var sample runtime.MemStats
			runtime.ReadMemStats(&sample)
			if minute%60 == 0 {
				t.Logf("virtual_hour=%d heap_bytes=%d goroutines=%d", minute/60, sample.HeapAlloc, runtime.NumGoroutine())
			}
			if minute == 0 {
				base = sample
			} else if minute%60 == 0 && sample.HeapAlloc > base.HeapAlloc+8<<20 {
				t.Fatalf("heap grew %d -> %d", base.HeapAlloc, sample.HeapAlloc)
			}
			if minute < 480 {
				time.Sleep(time.Minute)
			}
		}
		if cloud.allocs.Load() != 1 {
			t.Fatal("steady workload lost session")
		}
	})
}

func TestComposedOutageRecoveryAndRedial(t *testing.T) {

	for _, duration := range []time.Duration{30 * time.Second, 35 * time.Second} {
		t.Run(duration.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				defer cancel()
				var outage atomic.Bool
				s, stacks, cloud := workloadSession(t, ctx, func(p *simulatedWire) { p.outage = outage.Load })
				defer s.Close()
				c, err := s.DialPort(ctx, 4700)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				x := <-stacks
				if err = exchange(c.(*stream), 1, 0); err != nil {
					t.Fatal(err)
				}
				outage.Store(true)
				time.Sleep(duration)
				outage.Store(false)
				if duration == 30*time.Second {
					if err = exchange(c.(*stream), 2, 7900); err != nil {
						t.Fatal(err)
					}
					if cloud.allocs.Load() != 1 {
						t.Fatal("short outage unnecessarily replaced allocation")
					}
				} else {
					if _, err = io.ReadAll(c); !errors.Is(err, session.ErrTransport) {
						t.Fatalf("outage ended successfully: %v", err)
					}
					c, err = dialAfterRecovery(ctx, s)
					if err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					assertDTLSWorkersExited(t, x)
					x = <-stacks
					if err = exchange(c.(*stream), 3, 7900); err != nil {
						t.Fatal(err)
					}
					if cloud.allocs.Load() != 2 || cloud.stops.Load() != 1 {
						t.Fatalf("redial ownership %d/%d", cloud.allocs.Load(), cloud.stops.Load())
					}
				}
				inspectStack(t, x)
			})
		})
	}
}

func TestComposedVerifiedCancellationRaces(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s, stacks, _ := workloadSession(t, ctx, func(p *simulatedWire) { p.verify = true; p.delay = time.Millisecond })
		defer s.Close()
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		x := <-stacks
		for _, limit := range []time.Duration{time.Nanosecond, time.Millisecond, 10 * time.Millisecond, time.Second} {
			for range 8 {
				dial, stop := context.WithTimeout(ctx, limit)
				c, err := s.DialPort(dial, 4700)
				stop()
				if err != nil && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
				if c != nil {
					c.Close()
				}
				time.Sleep(50 * time.Millisecond)
				synctest.Wait()
				inspectStack(t, x)
			}
		}
	})
}
