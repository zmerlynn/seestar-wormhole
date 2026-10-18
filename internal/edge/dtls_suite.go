package edge

// Adapted from Pion v3.1.10's suite implementation; see NOTICE for the
// vendored upstream license.
// Pion owns ECDHE, PSK premaster, EMS, PRF and record AEAD;
// this adapter supplies only RFC 7905's suite metadata and 32/12-byte key sizes.
import (
	"crypto/sha256"
	"errors"
	"hash"
	"sync/atomic"

	dtls "github.com/pion/dtls/v3"
	cs "github.com/pion/dtls/v3/pkg/crypto/ciphersuite"
	"github.com/pion/dtls/v3/pkg/crypto/clientcertificate"
	"github.com/pion/dtls/v3/pkg/crypto/prf"
	"github.com/pion/dtls/v3/pkg/protocol/recordlayer"
)

const suiteID dtls.CipherSuiteID = 0xccac

type ecdhePSKChaCha struct {
	cipher atomic.Pointer[cs.ChaCha20Poly1305]
}

func (*ecdhePSKChaCha) String() string                          { return "TLS_ECDHE_PSK_WITH_CHACHA20_POLY1305_SHA256" }
func (*ecdhePSKChaCha) ID() dtls.CipherSuiteID                  { return suiteID }
func (*ecdhePSKChaCha) CertificateType() clientcertificate.Type { return 0 }
func (*ecdhePSKChaCha) HashFunc() func() hash.Hash              { return sha256.New }
func (*ecdhePSKChaCha) AuthenticationType() dtls.CipherSuiteAuthenticationType {
	return dtls.CipherSuiteAuthenticationTypePreSharedKey
}
func (*ecdhePSKChaCha) KeyExchangeAlgorithm() dtls.CipherSuiteKeyExchangeAlgorithm {
	return dtls.CipherSuiteKeyExchangeAlgorithmPsk | dtls.CipherSuiteKeyExchangeAlgorithmEcdhe
}
func (*ecdhePSKChaCha) ECC() bool             { return true }
func (s *ecdhePSKChaCha) IsInitialized() bool { return s.cipher.Load() != nil }

// Pion implements this key schedule and record protection with the suite sizes above:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md#key-schedule
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md#record-protection
func (s *ecdhePSKChaCha) Init(secret, client, server []byte, isClient bool) error {
	k, err := prf.GenerateEncryptionKeys(secret, client, server, 0, 32, 12, sha256.New)
	if err != nil {
		return err
	}
	lk, li, rk, ri := k.ClientWriteKey, k.ClientWriteIV, k.ServerWriteKey, k.ServerWriteIV
	if !isClient {
		lk, li, rk, ri = rk, ri, lk, li
	}
	c, err := cs.NewChaCha20Poly1305(lk, li, rk, ri)
	if err == nil {
		s.cipher.Store(c)
	}
	return err
}
func (s *ecdhePSKChaCha) Encrypt(p *recordlayer.RecordLayer, b []byte) ([]byte, error) {
	c := s.cipher.Load()
	if c == nil {
		return nil, errors.New("uninitialized")
	}
	return c.Encrypt(p, b)
}
func (s *ecdhePSKChaCha) Decrypt(h recordlayer.Header, b []byte) ([]byte, error) {
	c := s.cipher.Load()
	if c == nil {
		return nil, errors.New("uninitialized")
	}
	return c.Decrypt(h, b)
}

// NewECDHEPSKSuite returns fresh suite state for a maintained DTLS endpoint.
// The composition simulator uses the same suite metadata as the client.
func NewECDHEPSKSuite() dtls.CipherSuite { return &ecdhePSKChaCha{} }
