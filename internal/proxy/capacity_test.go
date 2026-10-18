package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"io"
	"net"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

func TestCapacityComposedPipelinedReads(t *testing.T) {

	for _, rate := range []int{0, 64 << 10} {
		t.Run(map[bool]string{true: "slow", false: "fast"}[rate > 0], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				s, stacks, cloud := workloadSessionLegacyCapacity(t, ctx, func(p *simulatedWire) {
					p.responseBytes = 1 << 20
					p.bulkBurst = true
					p.rdtGapAfter = 150
					p.rdtGapPackets = 700
				})
				defer s.Close()
				bulk, err := s.DialPort(ctx, 445)
				if err != nil {
					t.Fatal(err)
				}
				defer bulk.Close()
				rpc, err := s.DialPort(ctx, 4700)
				if err != nil {
					t.Fatal(err)
				}
				defer rpc.Close()
				x := <-stacks
				const cadence, deadline = time.Second, time.Second
				pollCtx, stopPolling := context.WithCancel(ctx)
				pollDone := make(chan capacityPollResult, 1)
				joined := false
				if rate > 0 {
					go capacityPollRPC(pollCtx, rpc, cadence, deadline, pollDone)
					defer func() {
						stopPolling()
						rpc.Close()
						if !joined {
							<-pollDone
						}
					}()
				} else {
					defer stopPolling()
				}
				started := time.Now()
				var reference [32]byte
				delivered := 0
				reader := bufio.NewReaderSize(bulk, 16<<10)
				if rate > 0 {
					reader = bufio.NewReaderSize(&capacityReader{reader: bulk, rate: rate}, 16<<10)
				}
				// Four opaque application requests outstanding at once, matching the
				// pressure of pipelined SMB READs without implementing an SMB parser.
				for batch := 0; batch < 4; batch++ {
					for i := 0; i < 4; i++ {
						if _, err := bulk.Write([]byte(rpcLine(99, 0))); err != nil {
							t.Fatal(err)
						}
					}
					for i := 0; i < 4; i++ {
						line, err := reader.ReadBytes('\n')
						if err != nil {
							t.Fatalf("after %d bytes: %v", delivered, err)
						}
						if err = checkResponse(string(line), 99); err != nil {
							t.Fatal(err)
						}
						digest := sha256.Sum256(line)
						if delivered == 0 {
							reference = digest
						} else if digest != reference {
							t.Fatal("content mismatch")
						}
						delivered += len(line)
						if rate == 0 {
							if err = exchange(rpc.(*stream), delivered, 0); err != nil {
								t.Fatalf("RPC after %d bytes: %v", delivered, err)
							}
						}
						inspectStack(t, x)
					}
				}
				if rate > 0 {
					elapsed := time.Since(started)
					want := time.Duration(delivered) * time.Second / time.Duration(rate)
					if elapsed < want || elapsed > want+5*time.Second {
						t.Fatalf("paced transfer elapsed=%s want=%s", elapsed, want)
					}
					stopPolling()
					polls := <-pollDone
					joined = true
					if polls.err != nil {
						t.Fatalf("RPC during transfer: %v", polls.err)
					}
					if polls.count < int(want/cadence)-2 || polls.worst > deadline || polls.first.Sub(started) > cadence+deadline || time.Since(polls.last) > cadence+deadline {
						t.Fatalf("concurrent RPC count=%d latency=%s first=%s last=%s transfer=%s", polls.count, polls.worst, polls.first.Sub(started), polls.last.Sub(started), elapsed)
					}
					t.Logf("RPC cadence=%s deadline=%s calls=%d max latency=%s transfer=%s", cadence, deadline, polls.count, polls.worst, elapsed)
				}
				if cloud.allocs.Load() != 1 {
					t.Fatal("bulk replaced session")
				}
				t.Logf("delivered=%d rate=%d; all 16 responses hashed, sibling RPC live", delivered, rate)
			})
		})
	}
}

type capacityReader struct {
	reader interface{ Read([]byte) (int, error) }
	rate   int
}

func (r *capacityReader) Read(b []byte) (int, error) {
	n, err := r.reader.Read(b[:min(len(b), 16<<10)])
	time.Sleep(time.Duration(n) * time.Second / time.Duration(r.rate)) // synctest virtual time
	return n, err
}

func TestCapacityComposedStuckReader(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		s, stacks, cloud := workloadSessionLegacyCapacity(t, ctx, func(p *simulatedWire) {
			p.responseBytes = 1 << 20
			p.bulkBurst = true
			p.rdtGapAfter = 150
			p.rdtGapPackets = 700
		})
		defer s.Close()
		bulk, err := s.DialPort(ctx, 445)
		if err != nil {
			t.Fatal(err)
		}
		defer bulk.Close()
		rpc, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer rpc.Close()
		x := <-stacks
		for i := 0; i < 4; i++ {
			if _, err := bulk.Write([]byte(rpcLine(99, 0))); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 36; i++ {
			if err := exchange(rpc.(*stream), i, 0); err != nil {
				t.Fatal(err)
			}
			inspectStack(t, x)
			time.Sleep(5 * time.Second) // injected synctest clock, never wall-clock waiting
		}
		if cloud.allocs.Load() != 1 {
			t.Fatal("stuck reader replaced session")
		}
		state := make(chan p2ptunnel.ChannelState, 1)
		x.post(func(core.Instant) { state <- x.tunnel.Channel(1) })
		ch := <-state
		if !ch.Active || !ch.SentPause || ch.Queued < 64<<10 {
			t.Fatalf("reader did not block and pause its own channel: %+v", ch)
		}
		t.Logf("180 virtual seconds, 36 sibling RPCs, stuck queue=%d", ch.Queued)
	})
}

func TestCapacityComposedHTTPFile(t *testing.T) {

	const volume = 16594560
	body := bytes.Repeat([]byte{0x17}, volume)
	expected := sha256.Sum256(body)
	var wire bytes.Buffer
	response := &http.Response{StatusCode: http.StatusOK, ProtoMajor: 1, ProtoMinor: 1, Header: make(http.Header), ContentLength: volume, Body: io.NopCloser(bytes.NewReader(body))}
	if err := response.Write(&wire); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		s, stacks, cloud := workloadSessionLegacyCapacity(t, ctx, func(p *simulatedWire) {
			p.httpResponse = wire.Bytes()
			p.bulkBurst = true
			p.rdtGapAfter = 150
			p.rdtGapPackets = 700
		})
		defer s.Close()
		bulk, err := s.DialPort(ctx, 80)
		if err != nil {
			t.Fatal(err)
		}
		defer bulk.Close()
		rpc, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer rpc.Close()
		x := <-stacks
		request, _ := http.NewRequest(http.MethodGet, "http://fixture/file", nil)
		if err := request.Write(bulk); err != nil {
			t.Fatal(err)
		}
		reply, err := http.ReadResponse(bufio.NewReader(bulk), request)
		if err != nil {
			t.Fatal(err)
		}
		defer reply.Body.Close()
		if reply.StatusCode != http.StatusOK || reply.ContentLength != volume {
			t.Fatal("wrong HTTP fixture headers")
		}
		digest := sha256.New()
		buf := make([]byte, 16<<10)
		received := 0
		for {
			n, err := reply.Body.Read(buf)
			digest.Write(buf[:n])
			received += n
			if received/(1<<20) != (received-n)/(1<<20) {
				if err := exchange(rpc.(*stream), received, 0); err != nil {
					t.Fatal(err)
				}
				inspectStack(t, x)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("HTTP body after %d/%d bytes: %v", received, volume, err)
			}
		}
		if received != volume || !bytes.Equal(digest.Sum(nil), expected[:]) {
			t.Fatal("truncated/corrupt HTTP body")
		}
		if cloud.allocs.Load() != 1 {
			t.Fatal("HTTP GET replaced session")
		}
		t.Logf("HTTP GET received and verified all %d bytes", received)
	})
}

// Each reader retains an exact share of the 8 MiB aggregate. At 127 stuck
// readers plus RPC this uses every admitted runtime slot. No bulk reads occur.
func TestCapacityComposedManyStuckReaders(t *testing.T) {

	for _, n := range []int{2, 4, 8, 32, 64, 127} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				share := (8 << 20) / n
				// The literal JSON fixture for ID 99 occupies 65 bytes around its padding.
				s, stacks, cloud := workloadSessionLegacyCapacity(t, ctx, func(p *simulatedWire) { p.bulkBurst = true; p.responseBytes = share - 65 })
				defer s.Close()
				x := <-stacks
				bulk := make([]net.Conn, n)
				for i := range bulk {
					var err error
					bulk[i], err = s.DialPort(ctx, 445)
					if err != nil {
						t.Fatal(err)
					}
					defer bulk[i].Close()
				}
				rpc, err := s.DialPort(ctx, 4700)
				if err != nil {
					t.Fatal(err)
				}
				defer rpc.Close()
				for i, c := range bulk {
					if _, err := c.Write([]byte(rpcLine(99, 0))); err != nil {
						t.Fatal(err)
					}
					until := time.Now().Add(10 * time.Second)
					for {
						u := capacityTunnelSnapshot(t, x)
						if u.RXBytes == (i+1)*share {
							break
						}
						if time.Now().After(until) {
							t.Fatalf("reader %d did not retain burst: %+v", i, u)
						}
						time.Sleep(10 * time.Millisecond) // synctest virtual time
					}
				}
				before := capacityTunnelSnapshot(t, x)
				if before.Channels != n+1 || before.RXBytes != n*share {
					t.Fatalf("stuck readers: %+v", before)
				}
				// The reply exceeds the remaining bytes, exercising aggregate
				// admission at an empty RPC, even when the share division leaves a tail.
				if _, err := capacityRPC(rpc, 1, 0, time.Second); err != nil {
					t.Fatalf("%d stuck readers closed empty RPC: %v", n, err)
				}
				after := capacityTunnelSnapshot(t, x)
				if after.Channels != n || after.RXBytes != (n-1)*share {
					t.Fatalf("backlog not reclaimed: %+v", after)
				}
				for i, c := range bulk {
					closed := false
					select {
					case <-c.(*stream).done:
						closed = true
					default:
					}
					if closed != (i == 0) {
						t.Fatalf("reader %d closed=%v want=%v", i, closed, i == 0)
					}
				}
				for i := 0; i < 3; i++ {
					if _, err := capacityRPC(rpc, i+2, 0, time.Second); err != nil {
						t.Fatal(err)
					}
					time.Sleep(time.Second)
				}
				inspectStack(t, x)
				if cloud.allocs.Load() != 1 {
					t.Fatal("aggregate pressure replaced session")
				}
				t.Logf("stuck=%d retained=%d victim=lowest channel ID; RPC continues on one allocation", n, before.RXBytes)
			})
		})
	}
}

// At the full runtime limit all 128 clients have a stuck delivery. The last
// client pipelines an RPC behind its 64 KiB reply, so both replies are already
// in RDT before PAUSE and pressure occurs while all 128 deliveries are retained.
func TestCapacityComposedAllChannelSlots(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s, stacks, cloud := workloadSessionLegacyCapacity(t, ctx, func(p *simulatedWire) { p.bulkBurst = true; p.responseBytes = (64 << 10) - 65 })
		defer s.Close()
		x := <-stacks
		var first, rpc net.Conn
		for i := 0; i < 128; i++ {
			c, err := s.DialPort(ctx, 445)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			rpc = c
			if i == 0 {
				first = c
			}
			request := rpcLine(99, 0)
			if i == 127 {
				request += rpcLine(1, 0)
			}
			if _, err := c.Write([]byte(request)); err != nil {
				t.Fatal(err)
			}
			if i == 127 {
				break
			}
			until := time.Now().Add(10 * time.Second)
			for capacityTunnelSnapshot(t, x).RXBytes != (i+1)*(64<<10) {
				if time.Now().After(until) {
					t.Fatal("full-slot burst not retained")
				}
				time.Sleep(10 * time.Millisecond)
			}
		}
		if err := rpc.SetDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		// Wait without reading: pressure must be resolved before consumer progress.
		until := time.Now().Add(time.Second)
		for capacityTunnelSnapshot(t, x).Channels == 128 {
			if time.Now().After(until) {
				t.Fatal("aggregate pressure did not reclaim a backlog")
			}
			time.Sleep(time.Millisecond)
		}
		select {
		case <-rpc.(*stream).done:
			t.Fatal("last sibling selected instead of lowest-ID backlog")
		default:
		}
		select {
		case <-first.(*stream).done:
		default:
			t.Fatal("lowest-ID backlog not reclaimed")
		}
		u := capacityTunnelSnapshot(t, x)
		if u.Channels != 127 || u.RXBytes <= 127*(64<<10) || u.RXBytes > 127*(64<<10)+p2ptunnel.MaxData {
			t.Fatalf("full-slot reclamation: %+v", u)
		}
		reader := bufio.NewReader(rpc)
		for _, id := range []int{99, 1} {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if err := checkResponse(line, id); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := capacityRPC(rpc, 2, 0, time.Second); err != nil {
			t.Fatal(err)
		}
		inspectStack(t, x)
		if cloud.allocs.Load() != 1 {
			t.Fatal("full-slot pressure replaced session")
		}
	})
}

func capacityTunnelSnapshot(t *testing.T, x *stack) p2ptunnel.Usage {
	t.Helper()
	done := make(chan p2ptunnel.Usage, 1)
	if !x.post(func(core.Instant) { done <- x.tunnel.Usage() }) {
		t.Fatal("inspection canceled")
	}
	return <-done
}

// capacityRPC uses a per-call deadline rather than exchange's 30-second default.
func capacityRPC(c net.Conn, id, padding int, limit time.Duration) (time.Duration, error) {
	start := time.Now()
	if err := c.SetDeadline(start.Add(limit)); err != nil {
		return 0, err
	}
	if _, err := c.Write([]byte(rpcLine(id, padding))); err != nil {
		return time.Since(start), err
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err == nil {
		err = checkResponse(line, id)
	}
	return time.Since(start), err
}

type capacityPollResult struct {
	count       int
	worst       time.Duration
	first, last time.Time
	err         error
}

// One bounded sibling worker polls independently of bulk read completions.
// All timers and deadlines run in the enclosing synctest virtual clock.
func capacityPollRPC(ctx context.Context, c net.Conn, cadence, deadline time.Duration, done chan<- capacityPollResult) {
	var result capacityPollResult
	defer func() { done <- result }()
	ticker := time.NewTicker(cadence)
	defer ticker.Stop()
	for id := 1000; ; id++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		latency, err := capacityRPC(c, id, 0, deadline)
		if err != nil {
			result.err = err
			return
		}
		result.count++
		result.worst = max(result.worst, latency)
		result.last = time.Now()
		if result.count == 1 {
			result.first = result.last
		}
	}
}
