package iotcedge

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"math/big"

	"github.com/zmerlynn/seestar-wormhole/internal/edge"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
)

// ErrIOTCCrypto is sanitized; crypto errors must never expose connection material.
var ErrIOTCCrypto = errors.New("iotc: cryptographic effect failed")

func publicRSA(s string) (*rsa.PublicKey, error) {
	b, _ := pem.Decode([]byte(s))
	if b == nil {
		return nil, ErrIOTCCrypto
	}
	v, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return nil, ErrIOTCCrypto
	}
	k, ok := v.(*rsa.PublicKey)
	if !ok {
		return nil, ErrIOTCCrypto
	}
	return k, nil
}

// rawPublic is an unpadded RSA public operation, using math/big for
// public (nonsecret) exponentiation. No private RSA operation is implemented.
// The license and master envelopes require unpadded public operations:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/master-record.md#crypto-primitives
func rawPublic(k *rsa.PublicKey, b []byte) ([]byte, error) {
	if len(b) != k.Size() {
		return nil, ErrIOTCCrypto
	}
	n := new(big.Int).SetBytes(b)
	if n.Cmp(k.N) >= 0 {
		return nil, ErrIOTCCrypto
	}
	return new(big.Int).Exp(n, big.NewInt(int64(k.E)), k.N).FillBytes(make([]byte, k.Size())), nil
}

// VerifyIOTCLicense validates the signed envelope before exposing discovery
// settings.
// Verify before exposing the license JSON:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/sdk-licence.md#licence-envelope
func VerifyIOTCLicense(s string) (iotc.License, error) {
	k, err := publicRSA(iotc.LicensePublicKeyPEM)
	if err != nil {
		return iotc.License{}, err
	}
	return verifyLicense(s, k)
}
func verifyLicense(s string, k *rsa.PublicKey) (iotc.License, error) {
	block, payload, err := iotc.ParseLicenseEnvelope(s)
	if err != nil {
		return iotc.License{}, ErrIOTCCrypto
	}
	recovered, err := rawPublic(k, block[:])
	if err != nil {
		return iotc.License{}, err
	}
	plain, err := iotc.UnmaskLicense(payload, [128]byte(recovered))
	if err != nil {
		return iotc.License{}, ErrIOTCCrypto
	}
	digest := sha256.Sum256(plain)
	digest[0] = 0x11
	if subtle.ConstantTimeCompare(digest[:], recovered[:32]) != 1 {
		return iotc.License{}, ErrIOTCCrypto
	}
	l, err := iotc.ParseLicenseJSON(plain)
	if err != nil {
		return iotc.License{}, ErrIOTCCrypto
	}
	return l, nil
}

type iotcKey struct {
	aead cipher.AEAD
	iv   [12]byte
}

// IOTCCrypto belongs to one attempt's serialized effect worker, never shared
// across retries. Its 64-key lifetime cap bounds malicious metadata churn.
type IOTCCrypto struct {
	// KeyLog optionally enqueues each new pair without affecting the session.
	KeyLog  *keylog.Writer
	rid     [8]byte
	private *ecdh.PrivateKey
	keys    map[iotc.KeyRef]iotcKey
	next    iotc.KeyRef
}

func (c *IOTCCrypto) install(key []byte, iv [12]byte) (iotc.KeyRef, error) {
	if len(c.keys) >= 64 {
		return 0, ErrIOTCCrypto
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return 0, ErrIOTCCrypto
	}
	a, err := cipher.NewGCM(b)
	if err != nil {
		return 0, ErrIOTCCrypto
	}
	if c.keys == nil {
		c.keys = make(map[iotc.KeyRef]iotcKey)
		c.next = 2
	}
	c.next++
	c.keys[c.next] = iotcKey{a, iv}
	return c.next, nil
}

// Execute performs a bounded crypto effect using crypto/rand. Only successfully
// authenticated plaintext returns Decrypted. Call Clear after joining the worker.
// Maintain the exact raw-RSA, ECDH slice and GCM AAD contract:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/rendezvous-gcm.md#cryptographic-primitives
func (c *IOTCCrypto) Execute(a iotc.Action) (iotc.Event, error) {
	e := iotc.Event{Attempt: a.Attempt, Token: a.Token, Link: a.Link}
	switch a.Kind {
	case iotc.Generate:
		if c.private != nil {
			return e, ErrIOTCCrypto
		}
		var m iotc.Material
		var err error
		if m.HelloSerial, err = edge.HelloSerialStart(rand.Reader); err != nil {
			return e, ErrIOTCCrypto
		}
		if m.RID, err = processRIDs.generate(rand.Reader); err != nil {
			return e, err
		}
		for _, b := range [][]byte{m.MasterKey[:], m.MasterIV[:]} {
			if _, err = io.ReadFull(rand.Reader, b); err != nil {
				return e, ErrIOTCCrypto
			}
		}
		var nonces [4]byte
		for m.MasterNonce == 0 || m.P2PNonce == 0 || m.MasterNonce == m.P2PNonce {
			if _, err = io.ReadFull(rand.Reader, nonces[:]); err != nil {
				return e, ErrIOTCCrypto
			}
			m.MasterNonce = binary.LittleEndian.Uint16(nonces[:])
			m.P2PNonce = binary.LittleEndian.Uint16(nonces[2:])
		}
		c.private, err = ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			return e, ErrIOTCCrypto
		}
		der, err := x509.MarshalPKIXPublicKey(c.private.PublicKey())
		if err != nil || len(der) != 91 {
			return e, ErrIOTCCrypto
		}
		copy(m.Public[:], der)
		m.PrivateRef = 1
		m.MasterRef, err = c.install(m.MasterKey[:], m.MasterIV)
		c.rid = m.RID
		if err == nil {
			_ = c.KeyLog.Rendezvous(a.Attempt, c.rid, uint64(m.MasterRef), m.MasterKey[:], m.MasterIV)
		}
		e.Kind = iotc.MaterialReady
		e.Material = m
		return e, err
	case iotc.Derive:
		if c.private == nil || a.Private != 1 {
			return e, ErrIOTCCrypto
		}
		v, err := x509.ParsePKIXPublicKey(a.Public[:91])
		if err != nil {
			return e, ErrIOTCCrypto
		}
		pub, ok := v.(*ecdsa.PublicKey)
		if !ok {
			return e, ErrIOTCCrypto
		}
		pk, err := pub.ECDH()
		if err != nil {
			return e, ErrIOTCCrypto
		}
		secret, err := c.private.ECDH(pk)
		if err != nil {
			return e, ErrIOTCCrypto
		}
		if _, err = io.ReadFull(rand.Reader, e.IV[:]); err != nil {
			return e, ErrIOTCCrypto
		}
		e.Key, err = c.install(secret[:16], e.IV)
		if err == nil {
			_ = c.KeyLog.Rendezvous(a.Attempt, c.rid, uint64(e.Key), secret[:16], e.IV)
		}
		clear(secret)
		e.Kind = iotc.Derived
		return e, err
	case iotc.Seal, iotc.Open:
		if a.Mode == iotc.RawMasterRSA {
			if a.Kind != iotc.Seal {
				return e, ErrIOTCCrypto
			}
			k, err := publicRSA(iotc.MasterPublicKeyPEM)
			if err != nil {
				return e, err
			}
			e.Data, err = rawPublic(k, a.Data)
			e.Kind = iotc.Encrypted
			return e, err
		}
		k, ok := c.keys[a.Key]
		if !ok {
			return e, ErrIOTCCrypto
		}
		if a.Kind == iotc.Seal {
			e.Kind = iotc.Encrypted
			e.Data = k.aead.Seal(nil, k.iv[:], a.Data, a.AAD)
			return e, nil
		}
		var err error
		e.Data, err = k.aead.Open(nil, k.iv[:], a.Data, a.AAD)
		if err != nil {
			e.Kind = iotc.CryptoRejected
			return e, nil
		}
		e.Kind = iotc.Decrypted
		return e, nil
	}
	return e, ErrIOTCCrypto
}

// Clear drops all attempt-local references after effects have stopped.
func (c *IOTCCrypto) Clear() { *c = IOTCCrypto{} }
