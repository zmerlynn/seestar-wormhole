package edge

import (
	"crypto/sha256"
	"errors"
	"hash"
	"sync"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/ciphersuite"
	"github.com/pion/dtls/v3/pkg/crypto/clientcertificate"
	"github.com/pion/dtls/v3/pkg/crypto/prf"
	"github.com/pion/dtls/v3/pkg/protocol/recordlayer"
)

// ecdhePSKChaCha connects Pion's public record cipher and key expansion APIs
// to the ECDHE_PSK suite defined in RFC 7905, section 2. Its zero value is
// uninitialized. It must not be copied after first use.
type ecdhePSKChaCha struct {
	// Serialize publication and all cipher use: the public record-cipher API
	// does not promise that its methods can be called concurrently.
	mu     sync.Mutex
	cipher *ciphersuite.ChaCha20Poly1305
}

var _ dtls.CipherSuite = (*ecdhePSKChaCha)(nil)

var errECDHEPSKUninitialized = errors.New("dtls: ECDHE-PSK ChaCha20-Poly1305 suite is uninitialized")

// NewECDHEPSKSuite returns a fresh, uninitialized cipher suite.
func NewECDHEPSKSuite() dtls.CipherSuite { return &ecdhePSKChaCha{} }

func (*ecdhePSKChaCha) String() string         { return "TLS_ECDHE_PSK_WITH_CHACHA20_POLY1305_SHA256" }
func (*ecdhePSKChaCha) ID() dtls.CipherSuiteID { return 0xCCAC }

// RFC 5489 authenticates the ephemeral ECDH exchange with a PSK, so no
// certificate type is used.
func (*ecdhePSKChaCha) CertificateType() clientcertificate.Type { return 0 }
func (*ecdhePSKChaCha) HashFunc() func() hash.Hash              { return sha256.New }
func (*ecdhePSKChaCha) AuthenticationType() dtls.CipherSuiteAuthenticationType {
	return dtls.CipherSuiteAuthenticationTypePreSharedKey
}
func (*ecdhePSKChaCha) KeyExchangeAlgorithm() dtls.CipherSuiteKeyExchangeAlgorithm {
	return dtls.CipherSuiteKeyExchangeAlgorithmEcdhe | dtls.CipherSuiteKeyExchangeAlgorithmPsk
}
func (*ecdhePSKChaCha) ECC() bool { return true }

func (s *ecdhePSKChaCha) Init(masterSecret, clientRandom, serverRandom []byte, isClient bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// RFC 5246, sections 6.2.3.3 and 6.3, and RFC 7905, section 2:
	// no MAC keys, two 256-bit write keys, and two 96-bit fixed IVs.
	// Pion owns the preceding (possibly extended) master-secret derivation.
	keys, err := prf.GenerateEncryptionKeys(masterSecret, clientRandom, serverRandom, 0, 32, 12, sha256.New)
	if err != nil {
		return err
	}
	localKey, localIV := keys.ClientWriteKey, keys.ClientWriteIV
	remoteKey, remoteIV := keys.ServerWriteKey, keys.ServerWriteIV
	if !isClient {
		localKey, remoteKey = remoteKey, localKey
		localIV, remoteIV = remoteIV, localIV
	}
	cipher, err := ciphersuite.NewChaCha20Poly1305(localKey, localIV, remoteKey, remoteIV)
	if err != nil {
		return err
	}
	// Publish only a completely constructed cipher. A failed Init preserves
	// the previous state (including an uninitialized zero value).
	s.cipher = cipher
	return nil
}

func (s *ecdhePSKChaCha) IsInitialized() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cipher != nil
}

func (s *ecdhePSKChaCha) Encrypt(pkt *recordlayer.RecordLayer, raw []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cipher == nil {
		return nil, errECDHEPSKUninitialized
	}
	return s.cipher.Encrypt(pkt, raw)
}

func (s *ecdhePSKChaCha) Decrypt(header recordlayer.Header, in []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cipher == nil {
		return nil, errECDHEPSKUninitialized
	}
	return s.cipher.Decrypt(header, in)
}
