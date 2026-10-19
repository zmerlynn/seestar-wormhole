package proxy

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"io"
)

// ErrSetupRejected means the peer explicitly refused tunnel SETUP verification.
var ErrSetupRejected = errors.New("proxy: peer rejected setup")

// PeerCloseError preserves the peer's one-byte CLOSE diagnostic. Error text
// contains no peer-controlled strings or identities.
type PeerCloseError struct{ Result byte }

func (e *PeerCloseError) Error() string {
	if e.Result == 2 {
		return ErrSetupRejected.Error()
	}
	return "proxy: peer closed with failure"
}
func (e *PeerCloseError) Unwrap() error {
	if e.Result == 2 {
		return ErrSetupRejected
	}
	return session.ErrTransport
}
func peerCloseCause(o *p2ptunnel.CloseOutcome) error {
	if o == nil {
		return session.ErrTransport
	}
	// Empty/zero is an ordinary close. Result 1 after our END also occurs
	// on ordinary peer EOF; the wire cannot distinguish that from terminal I/O
	// failure in this state. Before END it indicates connect/I/O failure.
	// Result 2 always means SETUP rejection, including after local half-close.
	if !o.HasResult || o.Result == 0 || o.Result == 1 && o.LocalEnd {
		return io.EOF
	}
	return &PeerCloseError{Result: o.Result}
}
