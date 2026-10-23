package proxy

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func Test300SequentialAdmissions(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		s := simulatedSession(t, ctx, Config{}, &simulatedCloud{})
		defer s.Close()
		for i := 0; i < 300; i++ {
			if i == 256 {
				// Leave the last 30 seconds of quarantine to bounded admission retries.
				time.Sleep(5*time.Minute - 30*time.Second)
			}
			c, err := s.DialPort(ctx, 4700)
			if err != nil {
				t.Fatalf("sequential client %d of 300: %v", i+1, err)
			}
			c.Close()
			synctest.Wait()
		}
	})
}

func TestQuarantinedAdmissionHonorsCancellation(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		s := simulatedSession(t, ctx, Config{}, &simulatedCloud{})
		defer s.Close()
		for i := 0; i < 256; i++ {
			c, err := s.DialPort(ctx, 4700)
			if err != nil {
				t.Fatal(err)
			}
			c.Close()
			synctest.Wait()
		}
		short, stop := context.WithTimeout(ctx, 100*time.Millisecond)
		if c, err := s.DialPort(short, 4700); !errors.Is(err, context.DeadlineExceeded) {
			if c != nil {
				c.Close()
			}
			t.Fatalf("pending cancellation: %v", err)
		}
		stop()
		synctest.Wait()
		// Retry near the floor so admission can succeed within its 35-second bound.
		time.Sleep(5*time.Minute - 30*time.Second)
		results := make(chan error, 4)
		for range 4 {
			go func() {
				c, err := s.DialPort(ctx, 4700)
				if c != nil {
					c.Close()
				}
				results <- err
			}()
		}
		for range 4 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
	})
}
