package iotc

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/x509"
	"fmt"
	"net/netip"
	"testing"
	"time"
)

type peerPair struct {
	aead cipher.AEAD
	iv   [12]byte
}

// Fixed scalars are test fixtures only. The server's scalar 1 corresponds to the
// P-256 generator already published in modern-fixtures.json's master response.
func testPrivate(t *testing.T, scalar byte) *ecdh.PrivateKey {
	t.Helper()
	raw := make([]byte, 32)
	raw[31] = scalar
	key, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
func parseTestPublic(slot []byte) (*ecdh.PublicKey, error) {
	if len(slot) != 92 {
		return nil, fmt.Errorf("public slot length")
	}
	key, err := x509.ParsePKIXPublicKey(slot[:91])
	if err != nil {
		return nil, err
	}
	ec, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an EC public key")
	}
	return ec.ECDH()
}
func testPair(private *ecdh.PrivateKey, public *ecdh.PublicKey, iv [12]byte) (peerPair, error) {
	secret, err := private.ECDH(public)
	if err != nil {
		return peerPair{}, err
	}
	block, err := aes.NewCipher(secret[:16])
	if err != nil {
		return peerPair{}, err
	}
	aead, err := cipher.NewGCM(block)
	return peerPair{aead, iv}, err
}

// requestPlain is the simulated server's receive boundary. It derives its own
// key from the published client public slot and consumes the published IV;
// it never reads the client's opaque key/IV handles.
func (h *harness) requestPlain(l Link, b []byte) ([]byte, error) {
	r, err := ParseRecord(b)
	if err != nil {
		return nil, err
	}
	if r.SID != 7 {
		return nil, fmt.Errorf("wrong record SID")
	}
	if r.Type == 1 {
		// RSA remains a structural stand-in, with complete plaintext/padding checks.
		if len(r.Body) != 256 || !bytes.Equal(r.Body[88:], make([]byte, 168)) {
			return nil, fmt.Errorf("master RSA block")
		}
		plain := r.Body[:88]
		return plain, h.validateRequest(plain)
	}
	pair := h.peerPairs[l.Address.Addr()]
	prefix := 16
	switch r.Type {
	case 2:
		if len(b) < 132 {
			return nil, fmt.Errorf("short published material")
		}
		public, err := parseTestPublic(b[24:116])
		if err != nil {
			return nil, err
		}
		iv := [12]byte(b[12:24])
		if iv == ([12]byte{}) {
			return nil, fmt.Errorf("zero published IV")
		}
		pair, err = testPair(h.serverPrivate, public, iv)
		if err != nil {
			return nil, err
		}
		prefix = 116
	case 4:
		if len(b) < 32 || !bytes.Equal(b[12:16], []byte{0x55, 0, 0, 0}) {
			return nil, fmt.Errorf("wrong server SID")
		}
	default:
		return nil, fmt.Errorf("unexpected request record type")
	}
	if pair.aead == nil {
		return nil, fmt.Errorf("missing published key agreement")
	}
	plain, err := pair.aead.Open(nil, pair.iv[:], b[prefix:], b[:prefix])
	if err != nil {
		return nil, err
	}
	if err = h.validateRequest(plain); err != nil {
		return plain, err
	}
	op := le16(plain[8:])
	if r.Type == 2 && op != 0x0217 || r.Type == 4 && (op == 0x0217 || op == 0x100b) {
		return nil, fmt.Errorf("inner/outer type mismatch")
	}
	h.peerPairs[l.Address.Addr()] = pair
	return plain, nil
}
func (h *harness) validateRequest(b []byte) error {
	if len(b) < 16 {
		return fmt.Errorf("short request")
	}
	want := h.requests[le16(b[8:])]
	// This peer offers one selected relay, requiring flag 1 on UDP/TCP.
	if want == nil || !bytes.Equal(b, want) {
		return fmt.Errorf("request %04x differs from literal fixture", le16(b[8:]))
	}
	return nil
}

func TestSimulatorRejectsMalformedRequests(t *testing.T) {
	h := newHarness(t, "direct", 0)
	cases := []struct {
		name string
		op   uint16
		off  int
	}{
		{"precheck nonce", 0x0217, 16}, {"precheck UID", 0x0217, 20},
		{"p2p RID", 0x0240, 100}, {"relay timeout", 0x020c, 44},
		{"relay UID", 0x020c, 16}, {"relay RID", 0x020c, 36},
		{"selected flag", 0x0241, 44}, {"ack allocation", 0x030f, 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p Packet
			if tc.op == 0x0217 {
				p = precheck(config().UID, material())
			} else {
				p = rendezvous(tc.op, config().UID, material(), 2, 0x2345, true)
			}
			b, _ := p.Marshal()
			if err := h.validateRequest(b); err != nil {
				t.Fatalf("valid request rejected: %v", err)
			}
			b[tc.off] ^= 1
			if h.validateRequest(b) == nil {
				t.Fatal("simulated peer accepted corrupted " + tc.name)
			}
		})
	}
}

func TestSimulatorConsumesPublishedMaterial(t *testing.T) {
	for _, field := range []string{"iv", "public"} {
		t.Run(field, func(t *testing.T) {
			h := newHarness(t, "direct", 0)
			h.step(Event{Kind: Start})
			h.run(time.Second)
			if h.m.state != Established {
				t.Fatal("setup did not establish")
			}
			q := precheck(h.m.cfg.UID, h.m.material)
			plain, _ := q.Marshal()
			aad, _ := RecordHeader(2, 7, 104+len(plain)+16)
			iv := h.ivs[3]
			aad = append(aad, iv[:]...)
			aad = append(aad, h.m.material.Public[:]...)
			if field == "iv" {
				clear(aad[12:24])
			} else {
				clear(aad[24:116])
			}
			b := append(aad, h.keys[3].Seal(nil, iv[:], plain, aad)...)
			l := Link{Address: netip.MustParseAddrPort("203.0.113.1:10001")}
			if _, err := h.requestPlain(l, b); err == nil {
				t.Fatal("simulated peer ignored published " + field)
			}
		})
	}
}
