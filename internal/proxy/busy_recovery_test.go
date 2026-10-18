package proxy

import (
	"context"
	"errors"
	"github.com/go-json-experiment/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

type recoveryBusyCloud struct {
	lazyBusyCloud
	lost  atomic.Bool
	mu    sync.Mutex
	dials []time.Time
}

func TestBusyListenerLogsOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var logs statsLogBuffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		s := simulatedSession(t, ctx, Config{Logger: logger}, &simulatedCloud{busy: true})
		defer s.Close()
		synctest.Wait()
		l := &admissionQueueListener{conns: make(chan net.Conn, 1), done: make(chan struct{})}
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, s, []Forward{{Listener: l, Port: 4700}}, 1, logger) }()
		for i := 0; i < 20; i++ {
			local, client := net.Pipe()
			l.conns <- local
			if _, err := client.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("down client=%v, want EOF", err)
			}
			client.Close()
			synctest.Wait()
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		logData := string(logs.take())
		if strings.Contains(logData, `"msg":"client_rejected"`) {
			t.Fatalf("per-client down-state logs: %s", logData)
		}
		if n := strings.Count(logData, `"phase":"backoff"`); n != 1 {
			t.Fatalf("busy backoff logs=%d, want 1", n)
		}
	})
}

func TestLazyBusyFreshClientRespectsMinimumInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tr := &lazyBusyCloud{route: "allocation"}
		s := lazyBusySession(t, t.Context(), Config{Idle: time.Minute}, tr)
		defer s.Close()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { _, err := s.DialPort(ctx, 4700); done <- err }()
		synctest.Wait()
		if tr.attempts.Load() != 1 {
			t.Fatal("initial client did not dial")
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		synctest.Wait()
		go func() { _, err := s.DialPort(t.Context(), 4700); done <- err }()
		synctest.Wait()
		if tr.attempts.Load() != 1 {
			t.Fatalf("fresh client bypassed minimum interval: calls=%d", tr.attempts.Load())
		}
		<-time.After(4 * time.Second)
		synctest.Wait()
		if tr.attempts.Load() < 2 {
			t.Fatal("fresh client never triggered another dial")
		}
		s.Close()
		<-done
	})
}

func (c *recoveryBusyCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	path := "/v1/my-device/detail"
	if c.route == "allocation" {
		path = "/v1/device-p2p/online-link"
	}
	if r.URL.Path == path {
		c.mu.Lock()
		c.dials = append(c.dials, time.Now())
		c.mu.Unlock()
	}
	return c.lazyBusyCloud.RoundTrip(r)
}

func recoveryBusySession(t *testing.T, ctx context.Context, cfg Config, tr *recoveryBusyCloud) *Session {
	t.Helper()
	c, err := newFixtureClient(t, cloudhttp.Config{Transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	s, err := startWithRNG(ctx, cfg, func(s *Session) sessionedge.Factory {
		return &factory{s: s, cloud: c, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner {
			trace := []string{}
			p := newSimulatedWire(t, &trace)
			p.outage = tr.lost.Load
			return p
		}}
	}, zeroRetryRNG{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAmbientBusyRecovery(t *testing.T) {
	for _, route := range []string{"precheck", "allocation"} {
		t.Run(route, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tr := &recoveryBusyCloud{lazyBusyCloud: lazyBusyCloud{route: route}}
				tr.unblocked.Store(true)
				var logs statsLogBuffer
				var s *Session
				label := "busy_recovery_test"
				pprof.Do(t.Context(), pprof.Labels(label, t.Name()), func(ctx context.Context) {
					s = recoveryBusySession(t, ctx, Config{Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, tr)
				})
				defer s.Close()
				c, err := s.DialPort(t.Context(), 4700)
				if err != nil {
					t.Fatal(err)
				}
				if err := exchange(c.(*stream), 1, 0); err != nil {
					t.Fatal(err)
				}
				if ownedWorkerCount(t, label, t.Name()) == 0 {
					t.Fatal("worker inventory did not identify the live session")
				}
				tr.unblocked.Store(false)
				tr.lost.Store(true)
				<-time.After(20 * time.Minute)
				synctest.Wait()
				c.Close()
				synctest.Wait()
				if tr.attempts.Load() < 20 {
					t.Fatalf("dial attempts=%d after 20m busy, want ongoing retries", tr.attempts.Load())
				}
				tr.mu.Lock()
				for i := 2; i < len(tr.dials); i++ {
					gap := tr.dials[i].Sub(tr.dials[i-1])
					if gap < 500*time.Millisecond || gap > 60*time.Second {
						t.Errorf("busy retry gap=%v, want 0.5s..60s", gap)
					}
				}
				tr.mu.Unlock()
				busy, summaries := 0, 0
				for _, line := range strings.Split(strings.TrimSpace(string(logs.take())), "\n") {
					var r struct{ Msg, Level, Reason string }
					if err := json.Unmarshal([]byte(line), &r, json.MatchCaseInsensitiveNames(true)); err != nil {
						t.Fatal(err)
					}
					if r.Msg == "layer_failed" && r.Reason == "busy" {
						busy++
						if r.Level != "INFO" {
							t.Errorf("busy level=%s, want INFO", r.Level)
						}
					}
					if r.Msg == "session_unavailable" && r.Level == "WARN" {
						summaries++
					}
				}
				if busy != int(tr.attempts.Load())-1 || summaries < 3 || summaries > 4 {
					t.Errorf("busy logs/summaries=%d/%d, attempts=%d", busy, summaries, tr.attempts.Load())
				}
				tr.lost.Store(false)
				tr.unblocked.Store(true)
				<-time.After(65 * time.Second)
				synctest.Wait()
				c, err = s.DialPort(t.Context(), 4700)
				if err != nil {
					t.Fatalf("recovered service: %v", err)
				}
				if err := exchange(c.(*stream), 2, 1024); err != nil {
					t.Fatal(err)
				}
				c.Close()
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if tr.allocs.Load() != 2 || tr.stops.Load() != 2 {
					t.Errorf("allocations/stops=%d/%d, want 2/2", tr.allocs.Load(), tr.stops.Load())
				}
				if n := ownedWorkerCount(t, label, t.Name()); n != 0 {
					t.Errorf("owned workers after close=%d, want 0", n)
				}
			})
		})
	}
}

// The new daemon has no readiness or release history. Cloud occupancy expires
// by virtual time alone, without a client request or a daemon restart.
func TestAmbientFirstBusyThenReady(t *testing.T) {
	for _, route := range []string{"precheck", "allocation"} {
		t.Run(route, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				began := time.Now()
				tr := &recoveryBusyCloud{lazyBusyCloud: lazyBusyCloud{route: route, ready: began.Add(20 * time.Minute)}}
				var logs statsLogBuffer
				s := recoveryBusySession(t, t.Context(), Config{Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, tr)
				defer s.Close()
				synctest.Wait()
				if tr.attempts.Load() != 1 || tr.allocs.Load() != 0 {
					t.Fatalf("first busy attempts/allocations=%d/%d, want 1/0", tr.attempts.Load(), tr.allocs.Load())
				}
				// Down clients cannot accelerate ambient backoff.
				for i := 0; i < 20; i++ {
					if _, err := s.DialPort(t.Context(), 4700); !errors.Is(err, session.ErrBusy) {
						t.Fatalf("busy client=%v, want ErrBusy", err)
					}
				}
				if tr.attempts.Load() != 1 {
					t.Fatal("local demand accelerated the retry")
				}
				<-time.After(20 * time.Minute)
				synctest.Wait()
				if tr.attempts.Load() < 40 {
					t.Fatalf("first busy attempts=%d after 20m, want ongoing retries", tr.attempts.Load())
				}
				busy, summaries := 0, 0
				for _, line := range strings.Split(strings.TrimSpace(string(logs.take())), "\n") {
					var r struct {
						Msg, Level, Reason string
						Elapsed            int64 `json:"unavailable_ms"`
					}
					if err := json.Unmarshal([]byte(line), &r, json.MatchCaseInsensitiveNames(true)); err != nil {
						t.Fatal(err)
					}
					if r.Msg == "layer_failed" && r.Reason == "busy" {
						busy++
						if r.Level != "INFO" {
							t.Errorf("busy level=%s, want INFO", r.Level)
						}
					}
					if r.Msg == "session_unavailable" {
						summaries++
						if r.Level != "WARN" || r.Elapsed != int64(summaries)*300000 {
							t.Errorf("summary=%+v, want WARN at %dm", r, summaries*5)
						}
					}
				}
				if busy != int(tr.attempts.Load()) || summaries != 4 {
					t.Errorf("busy logs/summaries=%d/%d, want %d/4", busy, summaries, tr.attempts.Load())
				}
				tr.mu.Lock()
				for i := 1; i < len(tr.dials); i++ {
					want := 15 * time.Second
					if i <= 5 {
						want = 500 * time.Millisecond * time.Duration(1<<(i-1))
					}
					if gap := tr.dials[i].Sub(tr.dials[i-1]); gap != want {
						t.Errorf("retry %d gap=%v, want %v", i, gap, want)
					}
				}
				tr.mu.Unlock()
				<-time.After(30 * time.Second)
				synctest.Wait()
				c, err := s.DialPort(t.Context(), 4700)
				if err != nil {
					t.Fatalf("first busy recovered service: %v", err)
				}
				if err := exchange(c.(*stream), 1, 1024); err != nil {
					t.Fatal(err)
				}
				c.Close()
				logs.take()
				<-time.After(5 * time.Minute)
				synctest.Wait()
				if strings.Contains(string(logs.take()), `"msg":"session_unavailable"`) {
					t.Fatal("warning after readiness")
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				if tr.allocs.Load() != 1 || tr.stops.Load() != 1 {
					t.Errorf("allocations/stops=%d/%d, want 1/1", tr.allocs.Load(), tr.stops.Load())
				}
			})
		})
	}
}
