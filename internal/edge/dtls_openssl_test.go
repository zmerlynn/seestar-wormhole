package edge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	profile "github.com/zmerlynn/seestar-wormhole/internal/dtls"
)

// OpenSSL is the independent handshake/record oracle; UDP exists ONLY in this
// fixture, on container loopback. The production client still sees IOTC packets.
func TestDTLSOpenSSL(t *testing.T) {
	if _, e := exec.LookPath("openssl"); e != nil {
		t.Fatal("OpenSSL is required for independent DTLS interop")
	}
	version, e := exec.Command("openssl", "version").CombinedOutput()
	if e != nil {
		t.Fatalf("OpenSSL version: %v: %s", e, version)
	}
	t.Logf("%s", version)
	for _, tc := range []struct {
		name, ciphers string
		suite         uint16
	}{{"preferred", "ECDHE-PSK-CHACHA20-POLY1305:PSK-AES128-GCM-SHA256", 0xccac}, {"fallback", "PSK-AES128-GCM-SHA256", 0x00a8}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// Reserve an ephemeral loopback port, then release it for the subprocess.
			reservation, e := net.ListenPacket("udp4", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			addr := reservation.LocalAddr().String()
			reservation.Close()
			key := sha256.Sum256([]byte("password"))
			// Use OpenSSL's DTLS cookie listener and the fixture's IPv4 endpoint.
			// Both suites must still complete and exchange records.
			cmd := exec.CommandContext(ctx, "openssl", "s_server", "-4", "-accept", addr, "-dtls1_2", "-listen", "-nocert", "-psk", hex.EncodeToString(key[:]), "-psk_identity", "test", "-cipher", tc.ciphers, "-quiet", "-state", "-no_ticket")
			stdin, e := cmd.StdinPipe()
			if e != nil {
				t.Fatal(e)
			}
			stdout, e := cmd.StdoutPipe()
			if e != nil {
				t.Fatal(e)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if e = cmd.Start(); e != nil {
				t.Fatal(e)
			}
			defer func() {
				cancel()
				err := cmd.Wait()
				if t.Failed() {
					t.Logf("OpenSSL exit: %v; stderr: %s", err, &stderr)
				}
			}()
			udp, e := net.ListenPacket("udp4", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			remote, e := net.ResolveUDPAddr("udp4", addr)
			if e != nil {
				t.Fatal(e)
			}
			p := packetPath(t, 1388)
			var sent, received atomic.Uint32
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case d := <-p.Outgoing():
						if _, e := udp.WriteTo(d, remote); e != nil {
							return
						}
						sent.Add(1)
					}
				}
			}()
			go func() {
				defer wg.Done()
				b := make([]byte, 2048)
				for {
					n, _, e := udp.ReadFrom(b)
					if e != nil {
						return
					}
					received.Add(1)
					if p.Deliver(1, b[:n]) != nil {
						return
					}
				}
			}()
			defer func() { cancel(); udp.Close(); p.Close(); wg.Wait() }()
			// Windows s_server treats redirected stdin as ready even when the
			// pipe is empty, then blocks reading it before servicing the UDP socket.
			// Supply a greeting to drive its handshake, and queue each reply before
			// waiting for decrypted output. Both sides still verify both application sizes.
			greeting := []byte("ready\n")
			if _, e := stdin.Write(greeting); e != nil {
				t.Fatal(e)
			}
			c, e := DialDTLS(ctx, p, []byte("test"), []byte("password"))
			if e != nil {
				t.Fatalf("OpenSSL handshake: %v (UDP datagrams sent=%d received=%d)", e, sent.Load(), received.Load())
			}
			defer c.Close()
			if c.Profile() != (profile.Profile{Version: 0xfefd, Suite: tc.suite}) {
				t.Fatal(c.Profile())
			}
			b := make([]byte, 1300)
			// Two distinct application sizes catch record/AAD/nonce and boundary mistakes.
			for i, msg := range [][]byte{[]byte("ping\n"), append(bytes.Repeat([]byte{'x'}, 1299), '\n')} {
				if _, e = c.Write(msg); e != nil {
					t.Fatal(e)
				}
				reply := []byte("pong\n")
				if len(msg) == 1300 {
					reply = append(bytes.Repeat([]byte{'y'}, 1299), '\n')
				}
				if _, e = stdin.Write(reply); e != nil {
					t.Fatal(e)
				}
				got := make([]byte, len(msg))
				if _, e = io.ReadFull(stdout, got); e != nil || !bytes.Equal(got, msg) {
					t.Fatalf("OpenSSL decrypt mismatch: %v", e)
				}
				n, e := c.Read(b)
				// The greeting can arrive before application admission opens and be
				// dropped. If it arrives afterward, consume it before the real reply.
				if i == 0 && e == nil && bytes.Equal(b[:n], greeting) {
					n, e = c.Read(b)
				}
				if e != nil || !bytes.Equal(b[:n], reply) {
					t.Fatalf("OpenSSL encrypt mismatch: got %d bytes, want %d: %v", n, len(reply), e)
				}
			}
			t.Logf("OpenSSL DTLS 1.2 / 0x%04x: bidirectional short and 1300-byte application records verified", tc.suite)
		})
	}
}
