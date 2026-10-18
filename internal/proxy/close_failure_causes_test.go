package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"io"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
)

func TestMixedPeerCloseAndTunnelFailure(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	for _, result := range []byte{0, 1, 2, 255} {
		t.Run(fmt.Sprint(result), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var logs bytes.Buffer
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s, stacks, _ := workloadSession(t, ctx, func(*simulatedWire) {}, Config{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
				c, err := s.DialPort(ctx, 445)
				if err != nil {
					t.Fatal(err)
				}
				x := <-stacks
				sibling, err := s.DialPort(ctx, 4700)
				if err != nil {
					t.Fatal(err)
				}
				x.post(func(now core.Instant) {
					data := []byte{4, c.(*stream).channel, 1, 0, result, 2, 1, 255, 255}
					x.drive(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.Stream, Data: data}}, now)
				})
				_, err = c.Read(make([]byte, 1))
				if result == 0 {
					if err != io.EOF {
						t.Errorf("clean peer CLOSE lost exact EOF: %T %v", err, err)
					}
				} else {
					var terminal *diagnostic.Terminal
					if errors.As(err, &terminal) {
						t.Errorf("independent peer CLOSE overwritten by stack failure: %v", err)
					}
					if !abnormalChannel(err) {
						t.Errorf("abnormal peer CLOSE made quiet: %v", err)
					}
				}
				_, err = sibling.Read(make([]byte, 1))
				if p, r := diagnostic.Fields(err, ""); p != "tunnel" || r != "invalid_frame" {
					t.Errorf("sibling cause=%s/%s, want tunnel/invalid_frame", p, r)
				}
				c.Close()
				sibling.Close()
				synctest.Wait()
				s.Close()
				wantWarn := 0
				if result != 0 {
					wantWarn = 1
				}
				if strings.Count(logs.String(), `"level":"ERROR"`) != 1 || strings.Count(logs.String(), `"level":"WARN"`) != wantWarn {
					t.Errorf("bad report counts: %s", &logs)
				}
			})
		})
	}
}
func TestComposedCancellationEveryPhase(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")

	for _, phase := range []string{"rendezvous", "dtls", "rdt", "tunnel", "stack"} {
		lost := 0
		for range 24 {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var logs bytes.Buffer
				s, stacks, _ := workloadSession(t, ctx, func(*simulatedWire) {}, Config{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
				c, err := s.DialPort(ctx, 445)
				if err != nil {
					t.Fatal(err)
				}
				x := <-stacks
				x.post(func(core.Instant) { x.failAt(phase, "timeout"); cancel() })
				<-s.Done()
				_, err = c.Read(make([]byte, 1))
				if p, r := diagnostic.Fields(err, ""); p != phase || r != "timeout" {
					t.Errorf("stream lost cause=%s/%s", p, r)
				}
				c.Close()
				closeErr := s.Close()
				if p, r := diagnostic.Fields(closeErr, ""); p != phase || r != "timeout" {
					t.Errorf("owner result lost cause=%s/%s", p, r)
				}
				if strings.Count(logs.String(), `"level":"ERROR"`) != 1 {
					lost++
				}
			})
		}
		if lost != 0 {
			t.Errorf("owner cancellation erased default-visible ready RDT failure in %d/24 schedules", lost)
		}
	}
}
