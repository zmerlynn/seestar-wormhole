// Regression probes exercise record construction and cancellation behavior.
package edge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	pion "github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/protocol"
	"github.com/pion/dtls/v3/pkg/protocol/recordlayer"
	profile "github.com/zmerlynn/seestar-wormhole/internal/dtls"
)

func TestReviewSlowReaderCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, fallback := range []bool{false, true} {
			c, s, cancel := pairedDTLS(t, 1388, false, fallback, false)
			for i := 0; i < 20; i++ {
				if _, err := s.Write([]byte("unread")); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
			}
			start := time.Now()
			cancel()
			c.Close()
			synctest.Wait()
			if elapsed := time.Since(start); elapsed != 0 {
				t.Fatalf("shutdown took %s in virtual time", elapsed)
			}
		}
	})
}
func TestReviewPreCanceledDial(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for i := 0; i < 100; i++ {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			p := packetPath(t, 1388)
			c, err := DialDTLS(ctx, p, []byte("test"), []byte("password"))
			if c != nil || err != context.Canceled {
				t.Fatalf("got %v %v", c, err)
			}
		}
	})
}

// A server may send application data after sending its Finished. Datagram
// reordering can deliver those records first; they must not block Finished.
func TestReviewReorderedEarlyApplication(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		for _, earlyCount := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("fallback_%t/early_%d", fallback, earlyCount), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
					defer cancel()
					a, b := packetPath(t, 1388), packetPath(t, 1388)
					var links sync.WaitGroup
					links.Add(2)
					defer func() { cancel(); a.Close(); b.Close(); links.Wait() }()
					fail := func(err error) {
						t.Errorf("reordered DTLS test link: %v", err)
						cancel()
					}
					go func() {
						defer links.Done()
						for {
							select {
							case <-ctx.Done():
								return
							case d := <-a.Outgoing():
								if err := b.Deliver(1, d); err != nil {
									if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
										fail(err)
									}
									return
								}
							}
						}
					}()
					go func() {
						defer links.Done()
						var held []byte
						apps := 0
						for {
							select {
							case <-ctx.Done():
								return
							case d := <-b.Outgoing():
								records, err := recordlayer.UnpackDatagram(d)
								if err != nil {
									fail(err)
									return
								}
								for _, record := range records {
									var h recordlayer.Header
									if err := h.Unmarshal(record); err != nil {
										fail(err)
										return
									}
									r := bytes.Clone(record)
									if earlyCount > 0 && h.ContentType == protocol.ContentTypeHandshake && h.Epoch == 1 {
										held = r
										continue
									}
									if err := a.Deliver(1, r); err != nil {
										if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
											fail(err)
										}
										return
									}
									if h.ContentType == protocol.ContentTypeApplicationData {
										apps++
										if apps == earlyCount && held != nil {
											if err := a.Deliver(1, held); err != nil {
												if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
													fail(err)
												}
												return
											}
											held = nil
										}
									}
								}
							}
						}
					}()
					id, key, _ := profile.Credentials([]byte("test"), []byte("password"))
					var opts []pion.ServerOption
					for _, o := range dtlsOptions(id, key, 1388) {
						opts = append(opts, o)
					}
					if fallback {
						opts = append(opts, pion.WithCipherSuites(pion.TLS_PSK_WITH_AES_128_GCM_SHA256))
					}
					s, err := pion.ServerWithOptions(b, dtlsAddr{}, opts...)
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
					done := make(chan error, 1)
					go func() {
						if err := s.HandshakeContext(ctx); err != nil {
							done <- err
							return
						}
						for i := 0; i < 2; i++ {
							if _, err := s.Write([]byte("early")); err != nil {
								done <- err
								return
							}
						}
						done <- nil
					}()
					start := time.Now()
					c, err := DialDTLS(ctx, a, []byte("test"), []byte("password"))
					if c != nil {
						defer c.Close()
					}
					if e := <-done; e != nil {
						t.Fatal(e)
					}
					if err != nil {
						t.Fatalf("handshake failed after %d application records overtook server Finished (elapsed %s): %v", earlyCount, time.Since(start), err)
					}
					if earlyCount == 2 {
						if _, err := s.Write([]byte("after Finished")); err != nil {
							t.Fatal(err)
						}
						buf := make([]byte, profile.MaxApplication)
						n, err := c.Read(buf)
						if err != nil || string(buf[:n]) != "after Finished" {
							t.Fatalf("early data escaped admission filter: %q, %v", buf[:n], err)
						}
					}
				})
			})
		}
	}
}
