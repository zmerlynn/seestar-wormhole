package edge

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

func TestDTLSLinkDecoderRejection(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire []byte
		drop bool
	}{
		{"truncated_record", []byte{22, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 1}, false},
		{"truncated_handshake", []byte{22, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1}, false},
		{"bad_version_before_loss", []byte{22, 0xfe, 0xfc, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				a, b := packetPath(t, 1388), packetPath(t, 1388)
				var fragmented atomic.Bool
				result := make(chan error, 2)
				wg := linkDTLS(ctx, a, b, tc.drop, nil, &fragmented, func(err error) {
					result <- err
					cancel()
				})
				defer func() { cancel(); a.Close(); b.Close(); wg.Wait() }()
				if _, err := a.WriteTo(tc.wire, nil); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				select {
				case err := <-result:
					if err == nil {
						t.Fatal("decoder rejection reported nil")
					}
					if ctx.Err() == nil {
						t.Fatal("decoder rejection did not cancel the link")
					}
				default:
					t.Fatal("malformed outgoing record did not reach the link's error owner")
				}
				if len(b.rx) != 0 {
					t.Fatal("rejected record reached the destination")
				}
			})
		})
	}
}
