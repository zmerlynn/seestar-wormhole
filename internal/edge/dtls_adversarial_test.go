// Regression probes exercise record construction and cancellation behavior.
package edge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
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
	"golang.org/x/crypto/chacha20poly1305"
)

// Independent TLS P_hash transcription; never used by production.
func reviewKeys(secret, client, server []byte) []byte {
	seed := append([]byte("key expansion"), server...)
	seed = append(seed, client...)
	mac := func(b []byte) []byte { h := hmac.New(sha256.New, secret); h.Write(b); return h.Sum(nil) }
	a := seed
	var out []byte
	for len(out) < 88 {
		a = mac(a)
		out = append(out, mac(append(append([]byte{}, a...), seed...))...)
	}
	return out[:88]
}
func TestReviewRecordConstruction(t *testing.T) {
	secret := bytes.Repeat([]byte{0x11}, 48)
	client := bytes.Repeat([]byte{0x22}, 32)
	server := bytes.Repeat([]byte{0x33}, 32)
	keys := reviewKeys(secret, client, server)
	for _, isClient := range []bool{true, false} {
		sender, receiver := &ecdhePSKChaCha{}, &ecdhePSKChaCha{}
		if err := sender.Init(secret, client, server, isClient); err != nil {
			t.Fatal(err)
		}
		if err := receiver.Init(secret, client, server, !isClient); err != nil {
			t.Fatal(err)
		}
		header := []byte{23, 0xfe, 0xfd, 0x12, 0x34, 1, 2, 3, 4, 5, 6, 0, 5}
		payload := []byte("hello")
		raw := append(append([]byte{}, header...), payload...)
		pkt := &recordlayer.RecordLayer{Header: recordlayer.Header{ContentType: protocol.ContentTypeApplicationData, Version: protocol.Version1_2, Epoch: 0x1234, SequenceNumber: 0x010203040506, ContentLen: 5}}
		got, err := sender.Encrypt(pkt, raw)
		if err != nil {
			t.Fatal(err)
		}
		key, iv := keys[:32], keys[64:76]
		if !isClient {
			key, iv = keys[32:64], keys[76:88]
		}
		nonce := append([]byte{}, iv...)
		for i := 0; i < 8; i++ {
			nonce[4+i] ^= header[3+i]
		}
		aad := append(append(append([]byte{}, header[3:11]...), header[:3]...), header[11:13]...)
		aead, _ := chacha20poly1305.New(key)
		want := append(append([]byte{}, header...), aead.Seal(nil, nonce, payload, aad)...)
		want[12] = 21
		if !bytes.Equal(got, want) {
			t.Fatalf("client=%v record=%x want=%x", isClient, got, want)
		}
		for _, pos := range []int{0, 2, 3, 4, 5, 10, 13, len(got) - 1} {
			bad := bytes.Clone(got)
			bad[pos] ^= 1
			if _, err := receiver.Decrypt(recordlayer.Header{}, bad); err == nil {
				t.Fatalf("accepted modified byte %d", pos)
			}
		}
		decoded, err := receiver.Decrypt(recordlayer.Header{}, bytes.Clone(got))
		if err != nil || !bytes.Equal(decoded[13:], payload) {
			t.Fatalf("decode %x %v", decoded, err)
		}
	}
}
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
