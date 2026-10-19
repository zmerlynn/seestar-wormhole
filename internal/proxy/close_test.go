package proxy

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"io"
	"testing"
)

func TestPeerCloseCause(t *testing.T) {
	for _, ended := range []bool{false, true} {
		for _, result := range []byte{0, 1, 2, 255} {
			o := &p2ptunnel.CloseOutcome{HasResult: true, Result: result, LocalEnd: ended}
			err := peerCloseCause(o)
			clean := result == 0 || result == 1 && ended
			if clean {
				if err != io.EOF {
					t.Fatalf("%+v: %v want EOF", o, err)
				}
			} else {
				var peer *PeerCloseError
				cause := session.ErrTransport
				if result == 2 {
					cause = ErrSetupRejected
				}
				if !errors.As(err, &peer) || peer.Result != result || !errors.Is(err, cause) {
					t.Fatalf("%+v: lost close cause: %v", o, err)
				}
			}
		}
	}
	if peerCloseCause(&p2ptunnel.CloseOutcome{}) != io.EOF {
		t.Fatal("empty close is not EOF")
	}
	if !errors.Is(peerCloseCause(nil), session.ErrTransport) {
		t.Fatal("missing outcome became success")
	}
}
