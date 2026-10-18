package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

func TestBusyBlockedOutageWarnings(t *testing.T) {
	for _, lazy := range []bool{false, true} {
		for _, partialBusy := range []bool{false, true} {
			for _, stopErr := range []error{session.ErrStop, session.ErrUncertain} {
				t.Run(fmt.Sprintf("lazy=%v/partial_busy=%v/%v", lazy, partialBusy, stopErr), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						var logs statsLogBuffer
						attempts := 0
						f := &pullBusyFactory{channels: make(chan *pullBusyChannels, 8)}
						f.allocate = func(context.Context) (sessionedge.Lease, error) {
							attempts++
							if partialBusy && attempts == 1 {
								return f, nil
							}
							f.stopError = stopErr
							if partialBusy {
								return f, session.ErrBusy
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
						c, err := s.DialPort(t.Context(), 445)
						if err != nil {
							t.Fatal(err)
						}
						c.Close()
						synctest.Wait()
						if !strings.Contains(string(logs.take()), `"phase":"ready"`) {
							t.Fatal("did not reach Ready")
						}
						lostAt := time.Now()
						(<-f.channels).events <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
						synctest.Wait()
						if !lazy && partialBusy {
							<-time.After(time.Second)
						}
						if lazy && partialBusy {
							if _, err := s.DialPort(t.Context(), 445); !errors.Is(err, session.ErrStop) {
								t.Fatalf("partial busy cleanup error=%v, want stop failure", err)
							}
						}
						synctest.Wait()
						if data := string(logs.take()); !strings.Contains(data, `"phase":"busy"`) {
							t.Fatalf("did not block failed cleanup: %s", data)
						}
						wantAttempts := 1
						if partialBusy {
							wantAttempts = 2
						}
						// No client input or layer completions during these twenty minutes.
						for i := 1; i <= 4; i++ {
							<-time.After(time.Until(lostAt.Add(time.Duration(i) * 5 * time.Minute)))
							synctest.Wait()
							data := logs.take()
							records := loggingRecords(t, bytes.NewBuffer(data))
							if len(records) != 1 || records[0]["msg"] != "session_unavailable" || records[0]["level"] != "WARN" || records[0]["unavailable_ms"] != float64(i*300000) {
								t.Fatalf("at %dm records=%s, want one WARN with original outage elapsed", i*5, data)
							}
							if attempts != wantAttempts || int(f.stops.Load()) != wantAttempts {
								t.Fatalf("blocked owner allocated/stopped again: %d/%d, want %d/%d", attempts, f.stops.Load(), wantAttempts, wantAttempts)
							}
						}
						if _, err := s.DialPort(t.Context(), 445); !errors.Is(err, session.ErrStop) {
							t.Fatalf("terminal client error=%v, want stop failure", err)
						}
						if err := s.Close(); !errors.Is(err, session.ErrStop) {
							t.Fatalf("close=%v, want stop failure", err)
						}
						<-time.After(5 * time.Minute)
						synctest.Wait()
						if strings.Contains(string(logs.take()), `"msg":"session_unavailable"`) {
							t.Fatal("warning after owner shutdown")
						}
					})
				})
			}
		}
	}
}

func TestRecoveryWarningsResetOnReadiness(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs statsLogBuffer
		f := &pullBusyFactory{channels: make(chan *pullBusyChannels, 8)}
		f.allocate = func(context.Context) (sessionedge.Lease, error) { return f, nil }
		s, err := startWithRNG(t.Context(), Config{Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, func(s *Session) sessionedge.Factory { f.s = s; return f }, zeroRetryRNG{})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		synctest.Wait()
		(<-f.channels).events <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
		<-time.After(time.Second)
		synctest.Wait()
		data := string(logs.take())
		if strings.Count(data, `"phase":"ready"`) != 2 {
			t.Fatalf("did not recover: %s", data)
		}
		<-time.After(6 * time.Minute)
		synctest.Wait()
		if strings.Contains(string(logs.take()), `"msg":"session_unavailable"`) {
			t.Fatal("warning after recovery")
		}
		f.stopError = session.ErrStop
		(<-f.channels).events <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
		synctest.Wait()
		logs.take()
		<-time.After(5 * time.Minute)
		synctest.Wait()
		records := loggingRecords(t, bytes.NewBuffer(logs.take()))
		if len(records) != 1 || records[0]["msg"] != "session_unavailable" || records[0]["unavailable_ms"] != float64(300000) {
			t.Fatalf("second outage records=%v, want reset five-minute warning", records)
		}
	})
}

func TestLazyBusyWarningsRequireUnintentionalLossAfterReadiness(t *testing.T) {
	for _, idleRelease := range []bool{false, true} {
		t.Run(fmt.Sprint(idleRelease), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var logs statsLogBuffer
				f := &pullBusyFactory{channels: make(chan *pullBusyChannels, 32)}
				f.allocate = func(context.Context) (sessionedge.Lease, error) {
					if idleRelease {
						return f, nil
					}
					return nil, session.ErrBusy
				}
				cfg := Config{Idle: time.Hour, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
				if idleRelease {
					cfg.Idle = time.Second
				}
				s, err := startWithRNG(t.Context(), cfg, func(s *Session) sessionedge.Factory { f.s = s; return f }, zeroRetryRNG{})
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				if !idleRelease {
					if _, err := s.DialPort(t.Context(), 445); !errors.Is(err, session.ErrBusy) {
						t.Fatal(err)
					}
				}
				if idleRelease {
					c, err := s.DialPort(t.Context(), 445)
					if err != nil {
						t.Fatal(err)
					}
					c.Close()
				}
				<-time.After(20 * time.Minute)
				synctest.Wait()
				if strings.Contains(string(logs.take()), `"msg":"session_unavailable"`) {
					t.Fatal("warning without an unintentional loss after readiness")
				}
			})
		})
	}
}

func TestBusyWarningsAfterFailedIdleCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs statsLogBuffer
		f := &pullBusyFactory{channels: make(chan *pullBusyChannels, 8), stopError: session.ErrStop}
		f.allocate = func(context.Context) (sessionedge.Lease, error) { return f, nil }
		s, err := startWithRNG(t.Context(), Config{Idle: time.Second, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, func(s *Session) sessionedge.Factory { f.s = s; return f }, zeroRetryRNG{})
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		c, err := s.DialPort(t.Context(), 445)
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
		<-time.After(time.Second)
		synctest.Wait()
		logs.take()
		<-time.After(5 * time.Minute)
		synctest.Wait()
		records := loggingRecords(t, bytes.NewBuffer(logs.take()))
		if len(records) != 1 || records[0]["msg"] != "session_unavailable" || records[0]["level"] != "WARN" || records[0]["unavailable_ms"] != float64(300000) {
			t.Fatalf("failed idle cleanup records=%v, want a five-minute WARN after ownership block", records)
		}
		if f.stops.Load() != 1 {
			t.Fatalf("stop calls=%d, want 1", f.stops.Load())
		}
	})
}
