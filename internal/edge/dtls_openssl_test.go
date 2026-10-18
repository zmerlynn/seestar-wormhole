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
			cmd := exec.CommandContext(ctx, "openssl", "s_server", "-accept", addr, "-dtls1_2", "-nocert", "-psk", hex.EncodeToString(key[:]), "-psk_identity", "test", "-cipher", tc.ciphers, "-quiet", "-no_ticket")
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
			defer func() { cancel(); cmd.Wait() }()
			udp, e := net.ListenPacket("udp4", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			remote, e := net.ResolveUDPAddr("udp4", addr)
			if e != nil {
				t.Fatal(e)
			}
			p := packetPath(t, 1388)
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
					if p.Deliver(1, b[:n]) != nil {
						return
					}
				}
			}()
			defer func() { cancel(); udp.Close(); p.Close(); wg.Wait() }()
			c, e := DialDTLS(ctx, p, []byte("test"), []byte("password"))
			if e != nil {
				t.Fatalf("OpenSSL handshake: %v", e)
			}
			defer c.Close()
			if c.Profile() != (profile.Profile{Version: 0xfefd, Suite: tc.suite}) {
				t.Fatal(c.Profile())
			}
			// Two distinct application sizes catch record/AAD/nonce and boundary mistakes.
			for _, msg := range [][]byte{[]byte("ping\n"), append(bytes.Repeat([]byte{'x'}, 1299), '\n')} {
				if _, e = c.Write(msg); e != nil {
					t.Fatal(e)
				}
				got := make([]byte, len(msg))
				if _, e = io.ReadFull(stdout, got); e != nil || !bytes.Equal(got, msg) {
					t.Fatalf("OpenSSL decrypt mismatch: %v", e)
				}
				reply := []byte("pong\n")
				if len(msg) == 1300 {
					reply = append(bytes.Repeat([]byte{'y'}, 1299), '\n')
				}
				if _, e = stdin.Write(reply); e != nil {
					t.Fatal(e)
				}
				b := make([]byte, 1300)
				n, e := c.Read(b)
				if e != nil || !bytes.Equal(b[:n], reply) {
					t.Fatalf("OpenSSL encrypt mismatch: got %d bytes, want %d: %v", n, len(reply), e)
				}
			}
			t.Logf("OpenSSL DTLS 1.2 / 0x%04x: bidirectional short and 1300-byte application records verified", tc.suite)
		})
	}
}
