package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	cs "github.com/pion/dtls/v3/pkg/crypto/ciphersuite"
	"github.com/pion/dtls/v3/pkg/crypto/prf"
	"github.com/pion/dtls/v3/pkg/protocol"
	"github.com/pion/dtls/v3/pkg/protocol/handshake"
	"github.com/pion/dtls/v3/pkg/protocol/recordlayer"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
)

type capturedKeyRecord struct {
	client bool
	data   []byte
}

// Only the key file and captured datagrams enter this oracle. It has no access
// to either endpoint's State, PSK, ECDH secret or cipher instance.
func decryptKeyCapture(t *testing.T, keys []byte, capture []capturedKeyRecord, suite uint16) []byte {
	t.Helper()
	var client, secret, server []byte
	scanner := bufio.NewScanner(bytes.NewReader(keys))
	for scanner.Scan() {
		var label string
		var cr, ms []byte
		if strings.HasPrefix(scanner.Text(), "CLIENT_RANDOM ") {
			if _, err := fmt.Sscanf(scanner.Text(), "%s %x %x", &label, &cr, &ms); err != nil {
				t.Fatal(err)
			}
			if len(cr) != 32 || len(ms) != 48 {
				t.Fatal("invalid NSS line")
			}
			client, secret = cr, ms
		}
	}
	if scanner.Err() != nil || len(secret) != 48 {
		t.Fatal("missing NSS master secret")
	}
	var encrypted [][]byte
	for _, datagram := range capture {
		records, err := recordlayer.UnpackDatagram(datagram.data)
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range records {
			var h recordlayer.Header
			if err := h.Unmarshal(raw); err != nil {
				t.Fatal(err)
			}
			if h.Epoch == 0 && h.ContentType == protocol.ContentTypeHandshake {
				var hh handshake.Header
				if err := hh.Unmarshal(raw[h.Size():]); err != nil {
					t.Fatal(err)
				}
				if hh.Type != handshake.TypeClientHello && hh.Type != handshake.TypeServerHello {
					continue
				}
				var r recordlayer.RecordLayer
				if err := r.Unmarshal(raw); err != nil {
					t.Fatal(err)
				}
				switch hello := r.Content.(*handshake.Handshake).Message.(type) {
				case *handshake.MessageClientHello:
					cr := hello.Random.MarshalFixed()
					if !bytes.Equal(client, cr[:]) {
						t.Fatal("keylog does not identify captured client")
					}
				case *handshake.MessageServerHello:
					sr := hello.Random.MarshalFixed()
					server = sr[:]
					if *hello.CipherSuiteID != suite {
						t.Fatal("unexpected suite")
					}
				}
			}
			if datagram.client && h.ContentType == protocol.ContentTypeApplicationData {
				encrypted = append(encrypted, raw)
			}
		}
	}
	if len(server) != 32 || len(encrypted) == 0 {
		t.Fatal("capture lacks hello or application record")
	}
	cipherFor := func(master []byte) interface {
		Decrypt(recordlayer.Header, []byte) ([]byte, error)
	} {
		keyLen, ivLen := 32, 12
		if suite == 0x00a8 {
			keyLen, ivLen = 16, 4
		}
		k, err := prf.GenerateEncryptionKeys(master, client, server, 0, keyLen, ivLen, sha256.New)
		if err != nil {
			t.Fatal(err)
		}
		if suite == 0x00a8 {
			c, err := cs.NewGCM(k.ServerWriteKey, k.ServerWriteIV, k.ClientWriteKey, k.ClientWriteIV)
			if err != nil {
				t.Fatal(err)
			}
			return c
		}
		c, err := cs.NewChaCha20Poly1305(k.ServerWriteKey, k.ServerWriteIV, k.ClientWriteKey, k.ClientWriteIV)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cipher := cipherFor(secret)
	var plain []byte
	for _, raw := range encrypted {
		var h recordlayer.Header
		if err := h.Unmarshal(raw); err != nil {
			t.Fatal(err)
		}
		b, err := cipher.Decrypt(h, append([]byte(nil), raw...))
		if err != nil {
			t.Fatal("independent captured-record authentication failed", err)
		}
		var r recordlayer.RecordLayer
		if err := r.Unmarshal(b); err != nil {
			t.Fatal(err)
		}
		plain = append(plain, r.Content.(*protocol.ApplicationData).Data...)
	}
	secret[0] ^= 1
	var h recordlayer.Header
	if err := h.Unmarshal(encrypted[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := cipherFor(secret).Decrypt(h, append([]byte(nil), encrypted[0]...)); err == nil {
		t.Fatal("wrong master secret authenticated")
	}
	return plain
}

func TestComposedKeylogDecryptAndNoOtherSecretOutput(t *testing.T) {

	for _, suite := range []uint16{0xccac, 0x00a8} {
		t.Run(fmt.Sprintf("%04x", suite), func(t *testing.T) {
			path := t.TempDir() + "/keys"
			keys, err := keylog.Open(path, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer keys.Close()
			var logs bytes.Buffer
			// Capture process output too: accidental Printf or default logging must fail.
			consoleRead, consoleWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			oldOut, oldErr := os.Stdout, os.Stderr
			os.Stdout, os.Stderr = consoleWrite, consoleWrite
			t.Cleanup(func() { os.Stdout, os.Stderr = oldOut, oldErr; consoleRead.Close(); consoleWrite.Close() })
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			oldLogger := slog.Default()
			slog.SetDefault(logger)
			t.Cleanup(func() { slog.SetDefault(oldLogger) })
			var capture []capturedKeyRecord
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				s, _, _ := workloadSession(t, ctx, func(p *simulatedWire) {
					p.suite = suite
					p.crypt.KeyLog = keys
					p.capture = func(client bool, b []byte) {
						capture = append(capture, capturedKeyRecord{client, append([]byte(nil), b...)})
					}
				}, Config{KeyLog: keys, Logger: logger})
				defer s.Close()
				c, err := s.DialPort(ctx, 4700)
				if err != nil {
					t.Fatal(err)
				}
				if err := exchange(c.(*stream), 73, 0); err != nil {
					t.Fatal(err)
				}
				c.Close()
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			})
			consoleWrite.Close()
			console, err := io.ReadAll(consoleRead)
			os.Stdout, os.Stderr = oldOut, oldErr
			if err != nil {
				t.Fatal(err)
			}
			if len(console) != 0 {
				t.Fatal("unexpected process output outside key writer and diagnostics")
			}
			keys.Close()
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(decryptKeyCapture(t, b, capture, suite), []byte(rpcLine(73, 0))) {
				t.Fatal("capture did not decrypt the actual RPC")
			}
			// Check all key/IV/master-secret fields, in raw and hex forms, against
			// diagnostic output. Peer output is authenticated ciphertext above.
			for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
				fields := strings.Fields(line)
				start := 2 // NSS secret; rendezvous has key and IV at 5 and 6.
				if fields[0] == "#" {
					start = 5
				}
				for _, field := range fields[start:] {
					raw, err := hex.DecodeString(field)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(logs.String(), field) || bytes.Contains(logs.Bytes(), raw) {
						t.Fatal("secret escaped key writer into diagnostics")
					}
				}
			}
			if bytes.Count(b, []byte("# IOTC_KEY ")) < 2 {
				t.Fatal("missing rendezvous material")
			}
		})
	}
}
