package edge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	pion "github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/protocol"
	"github.com/pion/dtls/v3/pkg/protocol/handshake"
	"github.com/pion/dtls/v3/pkg/protocol/recordlayer"
	profile "github.com/zmerlynn/seestar-wormhole/internal/dtls"
)

func packetPath(t *testing.T, mtu int) *DTLSPackets {
	t.Helper()
	p, e := NewDTLSPackets(mtu)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { p.Close() })
	return p
}
func TestDTLSPacketBounds(t *testing.T) {
	p := packetPath(t, 1388)
	p.allowApplication()
	b := []byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 42}
	if err := p.Deliver(0, b); err == nil {
		t.Fatal("plaintext accepted")
	}
	for i := 0; i < 32; i++ {
		if e := p.Deliver(1, b); e != nil {
			t.Fatal(e)
		}
	}
	if e := p.Deliver(1, b); !errors.Is(e, ErrDTLSQueueFull) {
		t.Fatal(e)
	}
	b[13] = 99
	buf := make([]byte, 1388)
	for i := 0; i < 32; i++ {
		n, _, e := p.ReadFrom(buf)
		if e != nil || n != 14 || buf[13] != 42 {
			t.Fatal("packet ownership/boundary", n, e)
		}
	}
	if e := p.Deliver(1, b); e != nil {
		t.Fatal(e)
	}
	if _, _, e := p.ReadFrom(buf[:1]); e != io.ErrShortBuffer {
		t.Fatal(e)
	}
	for i := 0; i < 32; i++ {
		if _, e := p.WriteTo(b, nil); e != nil {
			t.Fatal(e)
		}
	}
	p.SetWriteDeadline(time.Now().Add(-time.Second))
	if _, e := p.WriteTo(b, nil); !errors.Is(e, os.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	p.SetWriteDeadline(time.Time{})
	b[13] = 7 // The caller reuses its buffer after WriteTo returns.
	for i := 0; i < 32; i++ {
		got := <-p.Outgoing()
		if len(got) != 14 || got[13] != 99 {
			t.Fatalf("outbound packet changed after caller reuse: %x", got)
		}
	}
	if _, e := p.WriteTo(make([]byte, 1389), nil); e == nil {
		t.Fatal("MTU ignored")
	}
	p.Close()
	p.Close()
	if _, _, e := p.ReadFrom(buf); !errors.Is(e, net.ErrClosed) {
		t.Fatal(e)
	}
	if e := p.Deliver(1, b); !errors.Is(e, net.ErrClosed) {
		t.Fatal(e)
	}
}
func TestDTLSControlBudget(t *testing.T) {
	// Pion rejects a datagram consisting only of a zero-length record header.
	for _, size := range []int{14, 1388} {
		p := packetPath(t, 1388)
		b := make([]byte, size)
		copy(b, []byte{22, 0xfe, 0xfd})
		b[11] = byte((size - 13) >> 8)
		b[12] = byte(size - 13)
		count := 0
		for {
			e := p.Deliver(1, b)
			if e != nil {
				if !errors.Is(e, ErrDTLSBudget) {
					t.Fatal(e)
				}
				break
			}
			count++
			p.ReadFrom(make([]byte, 1388))
		}
		want := min(512, 256*1024/size)
		if count != want {
			t.Fatalf("admitted %d control datagrams of size %d, want %d", count, size, want)
		}
	}
}
func TestDTLSDeadlines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := packetPath(t, 1388)
		p.SetReadDeadline(time.Now().Add(time.Second))
		if _, _, e := p.ReadFrom(make([]byte, 1388)); !errors.Is(e, os.ErrDeadlineExceeded) {
			t.Fatal(e)
		}
		p.SetReadDeadline(time.Time{})
		done := make(chan error, 1)
		go func() { _, _, e := p.ReadFrom(make([]byte, 1388)); done <- e }()
		synctest.Wait()
		p.Close()
		if e := <-done; !errors.Is(e, net.ErrClosed) {
			t.Fatal(e)
		}
	})
}

// The test link has an explicit joined owner and deterministic loss/tampering.
// No reliable-stream substitute is used for the packet boundary.
func linkDTLS(ctx context.Context, a, b *DTLSPackets, drop bool, tamper *atomic.Bool, fragmented *atomic.Bool, fail func(error)) *sync.WaitGroup {
	var wg sync.WaitGroup
	for _, pair := range [][2]*DTLSPackets{{a, b}, {b, a}} {
		wg.Add(1)
		go func(src, dst *DTLSPackets) {
			defer wg.Done()
			first := true
			for {
				select {
				case <-ctx.Done():
					return
				case d := <-src.Outgoing():
					// Decode before applying scripted loss so library rejection
					// always reaches the test that owns this link.
					if tamper != nil {
						d = bytes.Clone(d)
					}
					records, err := recordlayer.UnpackDatagram(d)
					if err != nil {
						fail(err)
						return
					}
					for _, r := range records {
						var h recordlayer.Header
						if err := h.Unmarshal(r); err != nil {
							fail(err)
							return
						}
						if h.ContentType == protocol.ContentTypeHandshake && h.Epoch == 0 {
							var hh handshake.Header
							if err := hh.Unmarshal(r[h.Size():]); err != nil {
								fail(err)
								return
							}
							if hh.Length != hh.FragmentLength {
								fragmented.Store(true)
							}
						}
						// Corrupt exactly one application record; handshake traffic is intact.
						if tamper != nil && h.ContentType == protocol.ContentTypeApplicationData && tamper.CompareAndSwap(true, false) {
							r[len(r)-1] ^= 1
						}
					}
					if first && drop {
						first = false
						continue
					}
					first = false
					if err := dst.Deliver(1, d); err != nil {
						if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
							fail(err)
						}
						return
					}
				}
			}
		}(pair[0], pair[1])
	}
	return &wg
}
func pairedDTLS(t *testing.T, mtu int, drop bool, fallback bool, wrong bool, tamper ...*atomic.Bool) (*SecureDTLS, *pion.Conn, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	a, b := packetPath(t, mtu), packetPath(t, mtu)
	var corrupt *atomic.Bool
	if len(tamper) > 0 {
		corrupt = tamper[0]
	}
	var fragmented atomic.Bool
	wg := linkDTLS(ctx, a, b, drop, corrupt, &fragmented, func(err error) {
		t.Errorf("DTLS test link: %v", err)
		cancel()
	})
	t.Cleanup(func() { cancel(); a.Close(); b.Close(); wg.Wait() })
	identity := []byte("test")
	if mtu == 256 {
		identity = bytes.Repeat([]byte{'i'}, 255)
	}
	id, key, _ := profile.Credentials(identity, []byte("password"))
	var opts []pion.ServerOption
	for _, o := range dtlsOptions(id, key, mtu) {
		opts = append(opts, o)
	}
	if fallback {
		opts = append(opts, pion.WithCustomCipherSuites(func() []pion.CipherSuite { return nil }))
	}
	opts = append(opts, pion.WithPSKIdentityHint([]byte{}))
	opts = append(opts, pion.WithPSK(func(id []byte) ([]byte, error) {
		if !bytes.Equal(id, identity) {
			return nil, errors.New("identity rejected")
		}
		return key[:], nil
	}))
	server, e := pion.ServerWithOptions(b, dtlsAddr{}, opts...)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		err := server.HandshakeContext(ctx)
		if err == nil {
			b.allowApplication()
		}
		done <- err
	}()
	t.Cleanup(func() { b.Close(); server.Close() })
	password := []byte("password")
	if wrong {
		password = []byte("wrong")
	}
	client, e := DialDTLS(ctx, a, identity, password)
	if wrong {
		if e == nil {
			client.Close()
			t.Fatal("wrong key authenticated")
		}
		cancel()
		<-done
		return nil, nil, cancel
	}
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { client.Close() })
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if mtu == 256 && !fragmented.Load() {
		t.Fatal("fixture did not fragment a handshake message")
	}
	return client, server, cancel
}
func TestDTLSPaired(t *testing.T) {
	for _, tc := range []struct {
		name           string
		mtu            int
		drop, fallback bool
	}{{"ccac", 1388, false, false}, {"fallback", 1388, false, true}, {"fragmented_loss", 256, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c, s, _ := pairedDTLS(t, tc.mtu, tc.drop, tc.fallback, false)
				want := profile.CCAC
				if tc.fallback {
					want = profile.PSKAES128GCM
				}
				if c.Profile() != (profile.Profile{Version: 0xfefd, Suite: want}) {
					t.Fatal(c.Profile())
				}
				size := tc.mtu - 37
				if size > 1300 {
					size = 1300
				}
				for _, msg := range [][]byte{[]byte("first"), bytes.Repeat([]byte{0x5a}, size), []byte("third")} {
					if n, e := c.Write(msg); e != nil || n != len(msg) {
						t.Fatal(n, e)
					}
					buf := make([]byte, 1388)
					s.SetReadDeadline(time.Now().Add(time.Second))
					n, e := s.Read(buf)
					if e != nil || !bytes.Equal(buf[:n], msg) {
						t.Fatal("application packet mismatch", n, e)
					}
					if _, e = s.Write(msg); e != nil {
						t.Fatal(e)
					}
					n, e = c.Read(buf)
					if e != nil || !bytes.Equal(buf[:n], msg) {
						t.Fatal("reply mismatch", n, e)
					}
				}
				if _, e := c.Write(make([]byte, 1301)); e == nil {
					t.Fatal("oversized RDT packet accepted")
				}
				if tc.mtu == 256 {
					if _, e := c.Write(make([]byte, 256)); e == nil {
						t.Fatal("application MTU ignored")
					}
				}
			})
		})
	}
}
func TestDTLSWrongKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { pairedDTLS(t, 1388, false, false, true) })
}
func TestDTLSCancelTimeoutAndJoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// synctest.Test waits for every bubble goroutine to exit; leaked
		// Pion or adapter workers fail the bubble instead of sampling a global count.
		for _, cancelEarly := range []bool{true, false} {
			ctx, cancel := context.WithCancel(context.Background())
			p := packetPath(t, 1388)
			result := make(chan error, 1)
			start := time.Now()
			go func() { _, e := DialDTLS(ctx, p, []byte("test"), []byte("password")); result <- e }()
			synctest.Wait()
			if cancelEarly {
				cancel()
			} else {
				// synctest advances virtual time; this never sleeps on wall time.
				time.Sleep(30*time.Second - time.Nanosecond)
				synctest.Wait()
				select {
				case err := <-result:
					t.Fatalf("handshake ended before 30 seconds: %v", err)
				default:
				}
			}
			e := <-result
			wantElapsed := 30 * time.Second
			if cancelEarly {
				wantElapsed = 0
			}
			if elapsed := time.Since(start); elapsed != wantElapsed {
				t.Fatalf("handshake ended at %s, want %s", elapsed, wantElapsed)
			}
			want := context.DeadlineExceeded
			if cancelEarly {
				want = context.Canceled
			}
			if !errors.Is(e, want) {
				t.Fatalf("got %v want %v", e, want)
			}
			cancel()
			synctest.Wait()
		}

	})
}

func TestDTLSOfferAndNoResumption(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for i := 0; i < 2; i++ {
			ctx, cancel := context.WithCancel(context.Background())
			p := packetPath(t, 1388)
			done := make(chan error, 1)
			go func() { _, e := DialDTLS(ctx, p, []byte("test"), []byte("password")); done <- e }()
			d := <-p.Outgoing()
			cancel()
			if e := <-done; !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
			records, err := recordlayer.UnpackDatagram(d)
			if err != nil || len(records) != 1 {
				t.Fatalf("ClientHello records = %d, error %v, want one record", len(records), err)
			}
			var record recordlayer.RecordLayer
			if err := record.Unmarshal(records[0]); err != nil {
				t.Fatal(err)
			}
			h, ok := record.Content.(*handshake.Handshake)
			if !ok {
				t.Fatalf("ClientHello record content = %T", record.Content)
			}
			hello, ok := h.Message.(*handshake.MessageClientHello)
			if !ok || !hello.Version.Equal(protocol.Version1_2) || len(hello.SessionID) != 0 || len(hello.Cookie) != 0 || !slices.Equal(hello.CipherSuiteIDs, []uint16{0xccac, 0x00a8}) {
				t.Fatal("unexpected ClientHello profile, session ID or cookie")
			}
		}
	})
}
func TestDTLSConcurrentCloseAndRead(t *testing.T) {
	for i := 0; i < 100; i++ {
		p := packetPath(t, 1388)
		p.Deliver(1, []byte{23, 0xfe, 0xfd, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0})
		done := make(chan struct{})
		go func() { defer close(done); p.ReadFrom(make([]byte, 1388)) }()
		p.Close()
		<-done
		if e := p.SetReadDeadline(time.Now().Add(time.Hour)); !errors.Is(e, net.ErrClosed) {
			t.Fatal("deadline accepted after close", e)
		}
	}
}

func TestDTLSTamperAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, fallback := range []bool{false, true} {
			var corrupt atomic.Bool
			c, s, cancel := pairedDTLS(t, 1388, false, fallback, false, &corrupt)
			corrupt.Store(true)
			if _, e := c.Write([]byte("must never arrive")); e != nil {
				t.Fatal(e)
			}
			if _, e := c.Write([]byte("authenticated")); e != nil {
				t.Fatal(e)
			}
			buf := make([]byte, 1300)
			s.SetReadDeadline(time.Now().Add(time.Second))
			n, e := s.Read(buf)
			if e != nil || string(buf[:n]) != "authenticated" {
				t.Fatal("tampered record delivered or poisoned connection", n, e)
			}
			done := make(chan error, 1)
			go func() { _, e := c.Read(buf); done <- e }()
			synctest.Wait()
			cancel()
			if e := <-done; e == nil {
				t.Fatal("blocked reader survived parent cancellation")
			}
			c.Close()
			c.Close()
		}
	})
}
