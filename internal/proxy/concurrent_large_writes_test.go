package proxy

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestConcurrentLargeWrites(t *testing.T) {

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s := simulatedSession(t, ctx, Config{}, &simulatedCloud{})
		defer s.Close()
		clients := make([]*stream, 128)
		for i := range clients {
			c, err := s.DialPort(ctx, 4700)
			if err != nil {
				t.Fatal(err)
			}
			clients[i] = c.(*stream)
			defer c.Close()
		}
		results := make(chan error, 128)
		for _, c := range clients {
			go func() {
				b := []byte(`{"method":"get_device_state","padding":"` + strings.Repeat("a", 7900) + "\"}\n")
				_, err := c.Write(b)
				if err == nil {
					r := make([]byte, 128)
					_, err = c.Read(r)
				}
				results <- err
			}()
		}
		failed := 0
		var first error
		for range clients {
			if err := <-results; err != nil {
				failed++
				if first == nil {
					first = err
				}
			}
		}
		if failed > 0 {
			t.Fatalf("%d/128 bounded writers failed; first error: %v", failed, first)
		}
	})
}
