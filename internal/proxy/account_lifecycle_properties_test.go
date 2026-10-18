package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/session/sessionedge"
)

// Each barrier is in the real account/allocation edge, including body reads
// after an allocation identifier is available for cleanup. No session phase
// mock can expose credentials being discarded by the account owner.
type phaseCloud struct {
	simulatedCloud
	path             string
	body             bool
	replays          int
	profiles         int
	reject           bool
	entered, release chan struct{}
	gated            atomic.Bool
}

func (c *phaseCloud) RoundTrip(r *http.Request) (*http.Response, error) {
	round := c.replays
	if r.URL.Path == "/v1/user/login" || r.URL.Path == "/v1/user/current-profile" {
		round = c.profiles
	}
	if r.URL.Path != c.path || round != c.replays || !c.gated.CompareAndSwap(false, true) {
		return c.respond(r)
	}
	if !c.body {
		close(c.entered)
		select {
		case <-c.release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return c.respond(r)
	}
	response, err := c.respond(r)
	response.Body = &phaseBody{ReadCloser: response.Body, ctx: r.Context(), entered: c.entered, release: c.release}
	return response, err
}

// Replay rounds are separate lifecycle phases even though their HTTP paths match.
// Each challenged profile is followed by a real credential replay in the owner.
func (c *phaseCloud) respond(r *http.Request) (*http.Response, error) {
	if c.reject {
		if r.URL.Path == "/v1/user/login" {
			c.logins.Add(1)
			return &http.Response{StatusCode: 403, Body: rejectedReplayBody{r.Context()}}, nil
		}
		if r.URL.Path == "/v1/user/current-profile" {
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"code":401,"data":null}`))}, nil
		}
	}
	if r.URL.Path == "/v1/user/current-profile" {
		c.profiles++
		if c.profiles <= c.replays {
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"code":401,"data":null}`))}, nil
		}
	}
	return c.simulatedCloud.RoundTrip(r)
}

type phaseBody struct {
	io.ReadCloser
	ctx              context.Context
	entered, release chan struct{}
	read             bool
}

func (b *phaseBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.EOF
	}
	b.read = true
	n, _ := b.ReadCloser.Read(p)
	close(b.entered)
	select {
	case <-b.release:
		return n, io.EOF
	case <-b.ctx.Done():
		return n, b.ctx.Err()
	}
}

func TestComposedAccountLifecycleProperties(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")

	paths := []string{"/v1/user/login", "/v1/user/current-profile", "/v1/my-device/detail", "/v1/device-p2p/online-link"}
	for _, seed := range []uint64{1, 7, 42} {
		// Shuffle every exhaustive cross product; never rely on a seed hitting a phase.
		rng := rand.New(rand.NewPCG(seed, 99))
		for _, index := range rng.Perm(len(paths) * 3 * 2 * 2) {
			path, replays, body, command := paths[index/12], index/4%3, index/2%2 != 0, index%2
			t.Run(fmt.Sprintf("%d/%s/replays=%d/body=%v/command=%d", seed, path, replays, body, command), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					tr := &phaseCloud{path: path, body: body, replays: replays, entered: make(chan struct{}), release: make(chan struct{})}
					client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: tr})
					if err != nil {
						t.Fatal(err)
					}
					var trace []string
					s, err := start(context.Background(), Config{Idle: time.Second}, func(s *Session) sessionedge.Factory {
						return &factory{s: s, cloud: client, device: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}, verify: func(string) (iotc.License, error) { return iotc.License{Realm: "test"}, nil }, wire: func(iotc.License) wireRunner { return newSimulatedWire(t, &trace) }}
					})
					if err != nil {
						client.Close()
						t.Fatal(err)
					}
					defer s.Close()
					admission := make(chan error, 1)
					go func() {
						conn, err := s.DialPort(t.Context(), 4700)
						if conn != nil {
							conn.Close()
						}
						admission <- err
					}()
					<-tr.entered
					switch command {
					case 0:
						close(tr.release)
					case 1:
						if err := s.Close(); err != nil && !((path == paths[0] || path == paths[1] || path == paths[3]) && errors.Is(err, session.ErrStop)) {
							t.Fatal(err)
						}
						synctest.Wait()
						if err := <-admission; !errors.Is(err, session.ErrClosed) {
							t.Fatalf("shutdown admission=%v", err)
						}
						closedLogins := tr.logins.Load()
						if err := client.Login(t.Context()); !errors.Is(err, cloud.ErrReauthentication) || tr.logins.Load() != closedLogins {
							t.Fatalf("shutdown revived account: error=%v logins=%d, want %d", err, tr.logins.Load(), closedLogins)
						}
						if tr.allocs.Load() != tr.stops.Load() {
							t.Fatal("shutdown leaked observed allocation")
						}
						return
					}
					if err := <-admission; err != nil {
						t.Fatalf("admission: %v", err)
					}
					<-time.After(2 * time.Second)
					synctest.Wait()
					wantLogins := int32(replays + 1)
					if tr.logins.Load() != wantLogins {
						t.Fatalf("logins=%d, want %d", tr.logins.Load(), wantLogins)
					}

					// After idle release, a received credential rejection must remain
					// terminal when new lazy demand attempts allocation.
					tr.reject = true
					err = client.CheckProfile(t.Context())
					if !errors.Is(err, cloud.ErrRejected) {
						t.Fatalf("rejection missing: %v", err)
					}
					rejectedLogins := tr.logins.Load()
					rejectedCtx, rejectedCancel := context.WithTimeout(t.Context(), 30*time.Second)
					rejectedConn, rejectedErr := s.DialPort(rejectedCtx, 4700)
					rejectedCancel()
					if rejectedConn != nil {
						rejectedConn.Close()
						t.Fatal("rejected credentials admitted a client")
					}
					if (!errors.Is(rejectedErr, session.ErrUncertain) && !errors.Is(rejectedErr, session.ErrStop)) || tr.logins.Load() != rejectedLogins {
						t.Fatalf("credential rejection retried: error=%v logins=%d, want %d", rejectedErr, tr.logins.Load(), rejectedLogins)
					}
					// Received rejection now blocks the composed adapter too, so its
					// joined cleanup may retain ErrStop for every request phase.
					if err := s.Close(); err != nil && !errors.Is(err, session.ErrStop) {
						t.Fatal(err)
					}
					synctest.Wait()
					if tr.allocs.Load() != tr.stops.Load() {
						t.Fatalf("allocations/stops=%d/%d", tr.allocs.Load(), tr.stops.Load())
					}
				})
			})
		}
	}
}
