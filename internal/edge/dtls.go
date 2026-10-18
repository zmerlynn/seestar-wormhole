package edge

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	pion "github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/elliptic"
	"github.com/pion/logging"
	profile "github.com/zmerlynn/seestar-wormhole/internal/dtls"
)

// ErrDTLSHandshake is deliberately sanitized: library errors may include peer
// identity material. Cancellation and deadline errors remain distinguishable.
var ErrDTLSHandshake = errors.New("dtls: handshake failed")

// ErrDTLSIO is a sanitized secure transport failure.
var ErrDTLSIO = errors.New("dtls: secure transport failed")

// SecureDTLS owns Pion and its cancellation watcher. Close joins every worker
// through the vendored lifecycle integration documented in PION-PATCH.md.
// The owner must call Close on every exit path. Read and Write each
// permit one worker.
// The parent context controls the entire lifetime, not only the handshake.
type SecureDTLS struct {
	conn    *pion.Conn
	packets *DTLSPackets
	profile profile.Profile
	stop    chan struct{}
	joined  chan struct{}
	once    sync.Once
}

// Offer a two-suite subset; Pion owns retries within the independent phase deadline:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md#version-and-cipher-suite
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/iotc/dtls.md#timers-and-shutdown
func dtlsOptions(identity []byte, key [32]byte, mtu int) []pion.Option {
	// Pion prepends custom suites. Each factory call must return fresh cipher state.
	return []pion.Option{
		pion.WithCipherSuites(pion.TLS_PSK_WITH_AES_128_GCM_SHA256),
		pion.WithCustomCipherSuites(func() []pion.CipherSuite { return []pion.CipherSuite{&ecdhePSKChaCha{}} }),
		pion.WithPSKIdentityHint(identity),
		pion.WithPSK(func([]byte) ([]byte, error) { return append([]byte(nil), key[:]...), nil }),
		pion.WithEllipticCurves(elliptic.X25519, elliptic.P256, elliptic.P384),
		pion.WithExtendedMasterSecret(pion.RequestExtendedMasterSecret),
		pion.WithFlightInterval(500 * time.Millisecond),
		// Pion fragments handshake bodies; reserve record13 + handshake12 + AEAD24.
		pion.WithMTU(mtu - 49),
		pion.WithSessionStore(nil), // No resumption or exported State/Resume entry point.
		pion.WithLoggerFactory(quietDTLSLogger{}),
	}
}

// DialDTLS performs a DTLS 1.2 PSK client handshake over the supplied IOTC path.
// Credentials are ephemeral JNI bytes (see dtls.Credentials); the password is
// hashed once and is not retained. Timeout is a distinct 30-second DTLS
// phase, or an earlier parent deadline. Errors never cause plaintext fallback.
// This call takes ownership of packets, including on failure.
func DialDTLS(ctx context.Context, packets *DTLSPackets, username, password []byte) (*SecureDTLS, error) {
	if packets == nil {
		return nil, ErrDTLSHandshake
	}
	identity, key, err := profile.Credentials(username, password)
	if err != nil {
		if errors.Is(err, profile.ErrCredentialNUL) && packets.Logger != nil {
			packets.Logger.Warn("dtls_credentials_refused", "rule", "R024")
		}
		packets.Close()
		return nil, err
	}
	var opts []pion.ClientOption
	for _, o := range dtlsOptions(identity, key, packets.mtu) {
		opts = append(opts, o)
	}
	conn, err := pion.ClientWithOptions(packets, dtlsAddr{}, opts...)
	if err != nil {
		packets.Close()
		return nil, ErrDTLSHandshake
	}
	s := &SecureDTLS{conn: conn, packets: packets, stop: make(chan struct{}), joined: make(chan struct{})}
	go func() {
		defer close(s.joined)
		select {
		case <-ctx.Done():
		case <-s.stop:
		}
		packets.Close()
		conn.Close()
	}()
	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err = conn.HandshakeContext(hctx)
	// The lifetime watcher can close Pion before cancellation propagates from
	// the parent to hctx. Check the parent directly, including when Pion returns
	// nil in its handshake-completion race.
	if err == nil {
		err = ctx.Err()
		if err == nil {
			err = hctx.Err()
		}
	}
	if err != nil {
		s.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if hctx.Err() != nil {
			return nil, hctx.Err()
		}
		return nil, ErrDTLSHandshake
	}
	// Pion v3.1.10 rejects ServerHello versions other than 1.2 in flight3handler.
	// Its State does not export version. This value is the library's authenticated
	// handshake guarantee, never inferred from an unauthenticated record header.
	state, ok := conn.ConnectionState()
	s.profile = profile.Profile{Version: profile.Version12, Suite: uint16(state.CipherSuiteID)}
	if !ok || !s.profile.Valid() {
		s.Close()
		return nil, ErrDTLSHandshake
	}
	packets.allowApplication()
	return s, nil
}

// Profile returns only authenticated public version and suite; no resumable state.
func (s *SecureDTLS) Profile() profile.Profile { return s.profile }

// Close uses quiet shutdown: close the packet path before
// Pion attempts close_notify, so blocked output can never delay cleanup.
func (s *SecureDTLS) Close() error { s.once.Do(func() { close(s.stop) }); <-s.joined; return nil }

// Read receives one authenticated application packet. Callers provide at least
// MaxApplication bytes. DTLS preserves packets and does not repair application loss.
func (s *SecureDTLS) Read(b []byte) (n int, result error) {
	if s.packets.trace != nil {
		inputLength := len(b)
		defer func() { s.packets.traceResult(16, inputLength, n, result) }()
	}
	if len(b) < profile.MaxApplication {
		return 0, io.ErrShortBuffer
	}
	n, err := s.conn.Read(b[:profile.MaxApplication])
	if err != nil {
		return 0, ErrDTLSIO
	}
	return n, nil
}

// Write sends exactly one complete RDT packet per application record. Oversized
// packets are rejected before writing, never fragmented into separate RDT packets.
func (s *SecureDTLS) Write(b []byte) (n int, result error) {
	if s.packets.trace != nil {
		inputLength := len(b)
		defer func() { s.packets.traceResult(18, inputLength, n, result) }()
	}
	overhead := 29
	if s.profile.Suite == profile.PSKAES128GCM {
		overhead = 37
	}
	if len(b) == 0 || len(b) > profile.MaxApplication || len(b) > s.packets.mtu-overhead {
		return 0, errors.New("dtls: application packet exceeds MTU")
	}
	n, err := s.conn.Write(b)
	if err != nil {
		return 0, ErrDTLSIO
	}
	return n, nil
}

// Pion trace logs may contain identity hints; suppress its logger completely.
// Session owners log only Profile and fixed failure categories.
type quietDTLSLogger struct{}

func (quietDTLSLogger) NewLogger(string) logging.LeveledLogger { return quietDTLSLogger{} }
func (quietDTLSLogger) Trace(string)                           {}
func (quietDTLSLogger) Tracef(string, ...any)                  {}
func (quietDTLSLogger) Debug(string)                           {}
func (quietDTLSLogger) Debugf(string, ...any)                  {}
func (quietDTLSLogger) Info(string)                            {}
func (quietDTLSLogger) Infof(string, ...any)                   {}
func (quietDTLSLogger) Warn(string)                            {}
func (quietDTLSLogger) Warnf(string, ...any)                   {}
func (quietDTLSLogger) Error(string)                           {}
func (quietDTLSLogger) Errorf(string, ...any)                  {}
