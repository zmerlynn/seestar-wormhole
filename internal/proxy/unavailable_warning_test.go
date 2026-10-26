package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

func TestRecoveryWarningsResetOnReadiness(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs logBuffer
		f := &busyFactory{channels: make(chan *busyChannels, 128)}
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
		f.allocate = func(context.Context) (sessionedge.Lease, error) { return nil, session.ErrBusy }
		(<-f.channels).events <- session.Event{Kind: session.Failed, Err: session.ErrTransport}
		synctest.Wait()
		logs.take()
		<-time.After(5 * time.Minute)
		synctest.Wait()
		records := loggingRecords(t, bytes.NewBuffer(logs.take()))
		records = slices.DeleteFunc(records, func(record map[string]any) bool { return record["msg"] != "session_unavailable" })
		if len(records) != 1 || records[0]["msg"] != "session_unavailable" || records[0]["unavailable_ms"] != float64(300000) {
			t.Fatalf("second outage records=%v, want reset five-minute warning", records)
		}
	})
}

func TestLazyBusyWarningsRequireUnintentionalLossAfterReadiness(t *testing.T) {
	for _, idleRelease := range []bool{false, true} {
		t.Run(fmt.Sprint(idleRelease), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var logs logBuffer
				f := &busyFactory{channels: make(chan *busyChannels, 32)}
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
		var logs logBuffer
		f := &busyFactory{channels: make(chan *busyChannels, 8), stopError: session.ErrStop}
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
		records = slices.DeleteFunc(records, func(record map[string]any) bool { return record["msg"] != "session_unavailable" })
		if len(records) != 1 || records[0]["msg"] != "session_unavailable" || records[0]["level"] != "WARN" || records[0]["unavailable_ms"] != float64(300000) {
			t.Fatalf("failed idle cleanup records=%v, want a five-minute WARN after ownership block", records)
		}
		if f.stops.Load() != 1 {
			t.Fatalf("stop calls=%d, want 1", f.stops.Load())
		}
	})
}
