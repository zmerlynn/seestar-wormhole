package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

func TestOwnershipRecoveryAvailability(t *testing.T) {
	for _, lazy := range []bool{false, true} {
		for _, ready := range []bool{false, true} {
			t.Run(fmt.Sprintf("lazy=%v/ready=%v", lazy, ready), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var logs logBuffer
					calls := 0
					f := &busyFactory{channels: make(chan *busyChannels, 128)}
					var recoverNow atomic.Bool
					f.allocate = func(context.Context) (sessionedge.Lease, error) {
						calls++
						if calls == 1 {
							if ready {
								return f, nil
							}
							return nil, session.ErrUncertain
						}
						if !recoverNow.Load() {
							return nil, session.ErrBusy
						}
						return f, nil
					}
					cfg := Config{Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
					if lazy {
						cfg.Idle = time.Hour
					}
					s, err := startWithRNG(t.Context(), cfg, func(s *Session) sessionedge.Factory { f.s = s; return f }, zeroRetryRNG{})
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
					if ready {
						c, err := s.DialPort(t.Context(), 445)
						if err != nil {
							t.Fatal(err)
						}
						c.Close()
						synctest.Wait()
						f.stopError = session.ErrStop
						(<-f.channels).events <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
					} else if lazy {
						if _, err := s.DialPort(t.Context(), 445); !errors.Is(err, session.ErrUncertain) {
							t.Fatal(err)
						}
					}
					synctest.Wait()
					reason := "ownership_uncertain"
					if ready {
						reason = "release_failed"
					}
					records := loggingRecords(t, bytes.NewBuffer(logs.take()))
					found := false
					for _, r := range records {
						if r["msg"] == "session_state" && r["phase"] == "backoff" && r["reason"] == reason && r["retry_ms"] == float64(60000) {
							found = true
						}
					}
					if !found {
						t.Fatalf("no ownership delay transition: %v", records)
					}
					<-time.After(59 * time.Second)
					synctest.Wait()
					if calls != 1 {
						t.Fatalf("early recovery: calls=%d", calls)
					}
					if !lazy {
						if _, err := s.DialPort(t.Context(), 445); err == nil {
							t.Fatal("eager recovery held client")
						}
					}
					<-time.After(6*time.Minute - time.Second)
					synctest.Wait()
					// Eager keeps retrying; lazy stays dormant-equivalent without demand.
					if (!lazy && calls < 3) || (lazy && calls != 1) {
						t.Fatalf("recovery demand policy: calls=%d", calls)
					}
					records = loggingRecords(t, bytes.NewBuffer(logs.take()))
					warnings := 0
					dial := false
					for _, r := range records {
						if r["msg"] == "session_unavailable" {
							warnings++
							if r["reason"] != reason || r["level"] != "WARN" || r["unavailable_ms"] != float64(300000) {
								t.Errorf("outage lost original reason: %v", r)
							}
						}
						if r["msg"] == "session_state" && r["phase"] == "dialing" {
							dial = true
						}
					}
					if warnings != 1 || (!lazy && !dial) {
						t.Fatalf("warnings=%d fresh dial logged=%v", warnings, dial)
					}
					f.stopError = nil
					recoverNow.Store(true)
					result := make(chan struct {
						c   net.Conn
						err error
					}, 1)
					if lazy {
						go func() {
							c, err := s.DialPort(t.Context(), 445)
							result <- struct {
								c   net.Conn
								err error
							}{c, err}
						}()
					}
					<-time.After(31 * time.Second)
					synctest.Wait()
					if lazy {
						r := <-result
						if r.err != nil {
							t.Fatal(r.err)
						}
						r.c.Close()
					} else {
						c, err := s.DialPort(t.Context(), 445)
						if err != nil {
							t.Fatal(err)
						}
						c.Close()
					}
					logs.take()
					<-time.After(6 * time.Minute)
					synctest.Wait()
					for _, r := range loggingRecords(t, bytes.NewBuffer(logs.take())) {
						if r["msg"] == "session_unavailable" {
							t.Fatalf("warning after ready: %v", r)
						}
					}
					// Readiness must also retire the old ownership reason. Start an
					// unrelated transport outage and inspect its next reminder.
					for len(f.channels) > 1 {
						<-f.channels
					}
					recoverNow.Store(false)
					(<-f.channels).events <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
					synctest.Wait()
					<-time.After(5 * time.Minute)
					synctest.Wait()
					wantReason := "busy"
					if lazy {
						wantReason = "transport_failed"
					}
					warnings = 0
					for _, r := range loggingRecords(t, bytes.NewBuffer(logs.take())) {
						if r["msg"] == "session_unavailable" {
							warnings++
							if r["reason"] != wantReason {
								t.Errorf("stale ownership reason in second outage: %v, want %s", r, wantReason)
							}
						}
					}
					if warnings != 1 {
						t.Fatalf("second outage warnings=%d, want 1", warnings)
					}
					if err := s.Close(); err != nil {
						t.Fatalf("recovered cleanup poisoned shutdown: %v", err)
					}
				})
			})
		}
	}
}

func TestOwnershipRecoveryStop(t *testing.T) {
	for _, freshDial := range []bool{false, true} {
		t.Run(fmt.Sprint(freshDial), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				f := &busyFactory{channels: make(chan *busyChannels, 8), stopError: session.ErrStop}
				f.allocate = func(ctx context.Context) (sessionedge.Lease, error) {
					calls++
					if calls == 1 {
						return f, nil
					}
					<-ctx.Done()
					return f, ctx.Err()
				}
				s, err := startWithRNG(t.Context(), Config{}, func(s *Session) sessionedge.Factory { f.s = s; return f }, zeroRetryRNG{})
				if err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				(<-f.channels).events <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
				synctest.Wait()
				if freshDial {
					<-time.After(time.Minute)
					synctest.Wait()
					if calls != 2 {
						t.Fatal("fresh dial did not start")
					}
					f.stopError = nil
				}
				began := time.Now()
				err = s.Close()
				if !errors.Is(err, session.ErrStop) || time.Since(began) != 0 {
					t.Fatalf("close=%v elapsed=%v", err, time.Since(began))
				}
				var final bytes.Buffer
				diagnostic.LogFinal(slog.New(slog.NewJSONHandler(&final, nil)), "proxy failed", "proxy", err, err)
				records := loggingRecords(t, &final)
				if len(records) != 1 || records[0]["cleanup_reason"] != "release_failed" {
					t.Fatalf("lost cleanup reporting: %v", records)
				}
				want := int32(1)
				if freshDial {
					want = 2
				}
				if f.stops.Load() != want {
					t.Fatalf("stops=%d want %d", f.stops.Load(), want)
				}
				<-time.After(time.Hour)
				synctest.Wait()
				if calls != int(want) {
					t.Fatal("allocation after shutdown")
				}
			})
		})
	}
}

func TestRefusalCleanupOwnershipDelayLogs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs logBuffer
		f := &busyFactory{channels: make(chan *busyChannels, 128), stopError: session.ErrReleaseAbandoned}
		f.allocate = func(context.Context) (sessionedge.Lease, error) { return f, session.ErrBusy }
		s, err := startWithRNG(t.Context(), Config{Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, func(s *Session) sessionedge.Factory { f.s = s; return f }, zeroRetryRNG{})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		synctest.Wait()
		found := false
		for _, r := range loggingRecords(t, bytes.NewBuffer(logs.take())) {
			if r["msg"] == "session_state" && r["phase"] == "backoff" && r["reason"] == "release_failed" && r["retry_ms"] == float64(60000) {
				found = true
			}
		}
		if !found {
			t.Fatal("promoted refusal cleanup did not log ownership delay")
		}
		<-time.After(5 * time.Minute)
		synctest.Wait()
		warnings := 0
		for _, r := range loggingRecords(t, bytes.NewBuffer(logs.take())) {
			if r["msg"] == "session_unavailable" {
				warnings++
				if r["reason"] != "release_failed" {
					t.Fatalf("lost ownership reason: %v", r)
				}
			}
		}
		if warnings != 1 {
			t.Fatalf("warnings=%d", warnings)
		}
	})
}
