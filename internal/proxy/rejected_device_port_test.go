package proxy

import (
	"context"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"
)

func TestRejectedDevicePortIsNotSuccess(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s, _, _ := workloadSession(t, ctx, func(*simulatedWire) {})
		defer s.Close()
		c, err := s.DialPort(ctx, 12345)
		if err != nil {
			if !errors.Is(err, ErrSetupRejected) {
				t.Fatalf("SETUP rejection lost cause: %v", err)
			}
			return
		}
		defer c.Close()
		b, err := io.ReadAll(c)
		var closeErr *PeerCloseError
		if !errors.Is(err, ErrSetupRejected) || !errors.As(err, &closeErr) || closeErr.Result != 2 {
			t.Fatalf("device rejected SETUP with CLOSE result 2, but ReadAll returned %d bytes and %v", len(b), err)
		}
	})
}
