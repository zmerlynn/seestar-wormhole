package ui

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNativeStoppedDispatcher(t *testing.T) {
	for _, action := range []string{"quit", "activate"} {
		for _, overlap := range []bool{false, true} {
			name := action + "/in-flight"
			if overlap {
				name = action + "/Run-overlap"
			}
			t.Run(name, func(t *testing.T) {
				attempts := 1
				if overlap {
					attempts = 1000
				}
				for range attempts {
					ctx, cancel := context.WithCancel(t.Context())
					activate := make(chan struct{}, 1)
					queued, resume := make(chan struct{}, 1), make(chan struct{})
					// Model InvokeSync waiting for a callback that a stopped
					// native dispatcher cannot execute. Only test cleanup resumes it.
					invokeSync := func() { queued <- struct{}{}; <-resume }
					done := make(chan struct{})
					want := errors.New("Run stopped")
					go func() {
						defer close(done)
						err := runWindow(ctx, "synthetic", func() {}, activate, func(c windowCallbacks) windowActions {
							return windowActions{
								quit: invokeSync, setURL: func(string) { invokeSync() },
								unminimise: func() {}, show: func() {}, focus: func() {},
								run: func() error {
									c.started()
									if action == "quit" {
										cancel()
									} else {
										activate <- struct{}{}
									}
									if !overlap {
										<-queued
									}
									return want
								},
							}
						})
						if err != want {
							t.Errorf("Run result lost: %v", err)
						}
					}()
					blocked := false
					select {
					case <-done:
					case <-time.After(time.Second):
						blocked = true
					}
					close(resume)
					<-done
					cancel()
					if blocked {
						t.Fatal("Run return blocked on stopped native dispatcher")
					}
				}
			})
		}
	}
}
