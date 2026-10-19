package proxy

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestCanceledAdmissionBurst(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s, _, _ := workloadSession(t, ctx, func(*simulatedWire) {})
		defer s.Close()
		c, err := s.DialPort(ctx, 4700)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		dead, stop := context.WithCancel(ctx)
		stop()
		for i := 0; i < 1000; i++ {
			client, _ := s.DialPort(dead, 4700)
			if client != nil {
				client.Close()
			}
		}
		synctest.Wait()
		if err := exchange(c.(*stream), 1, 0); err != nil {
			t.Fatalf("1000 sequential already-canceled admissions killed an existing client: %v", err)
		}
	})
}
