package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// These controls exercise the actual default account deadline, not an immediate
// injected timeout. Publication trails cancellation by a finite transport unwind.
type accountDeadlineControl struct {
	t                  *testing.T
	cloud              *r5Cloud
	path, body         string
	status             int
	panicClose         bool
	calls              atomic.Int32
	joined             atomic.Bool
	completed, retried time.Time // only the serialized account/attempt workers write
}

func (c *accountDeadlineControl) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == c.path && c.calls.Add(1) == 1 {
		deadline, _ := r.Context().Deadline()
		if left := time.Until(deadline); left != 30*time.Second {
			c.t.Errorf("account deadline=%v want default 30s", left)
		}
		if c.body == "no response" {
			<-r.Context().Done()
			<-time.After(20 * time.Millisecond)
			c.completed = time.Now()
			c.joined.Store(true)
			return nil, r.Context().Err()
		}
		b := &accountDeadlineBody{Reader: strings.NewReader(c.body), ctx: r.Context(), c: c}
		status := c.status
		if status == 0 {
			status = 200
		}
		return &http.Response{StatusCode: status, Body: b}, nil
	}
	if c.calls.Load() > 0 && c.retried.IsZero() {
		if !c.joined.Load() {
			c.t.Error("new cloud work before pending account completion joined")
		}
		c.retried = time.Now()
	}
	return c.cloud.RoundTrip(r)
}

type accountDeadlineBody struct {
	*strings.Reader
	ctx context.Context
	c   *accountDeadlineControl
}

func (b *accountDeadlineBody) Read(p []byte) (int, error) {
	if b.Reader.Len() > 0 {
		return b.Reader.Read(p)
	}
	<-b.ctx.Done()
	<-time.After(20 * time.Millisecond)
	b.c.completed = time.Now()
	b.c.joined.Store(true)
	return 0, b.ctx.Err()
}
func (b *accountDeadlineBody) Close() error {
	if b.c.panicClose {
		panic("synthetic close failure")
	}
	return nil
}

func TestAccountDefaultDeadlineControls(t *testing.T) {
	for _, startup := range []bool{false, true} {
		for _, path := range []string{"/v1/user/login", "/v1/user/current-profile"} {
			for _, mode := range []string{"no response", "incomplete body", "valid body"} {
				name := path + "/" + mode
				if startup {
					name = "startup-auto-select" + name
				} else {
					name = "eager" + name
				}
				t.Run(name, func(t *testing.T) {
					r5Credentials(t)
					synctest.Test(t, func(t *testing.T) {
						ledger := &r5Cloud{stops: map[string]int{}}
						body := mode
						if mode == "incomplete body" {
							body = `{"code":`
						}
						if mode == "valid body" {
							body = r5Body(path, 0)
						}
						tr := &accountDeadlineControl{t: t, cloud: ledger, path: path, body: body}
						client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: tr, StateDir: t.TempDir()})
						if err != nil {
							t.Fatal(err)
						}
						defer client.Close()
						if startup {
							d, err := selectDevice(t.Context(), client, cloudhttp.Device{}, nil, zeroRetryRNG{})
							if err != nil || d.Serial != "sim" {
								t.Fatalf("startup=%v/%v want sim/nil", d, err)
							}
						}
						s := r5Session(t, t.Context(), client, ledger)
						defer s.Close()
						<-time.After(40 * time.Second)
						synctest.Wait()
						conn, err := s.DialPort(t.Context(), 4700)
						if err != nil {
							t.Fatal(err)
						}
						defer conn.Close()
						if err = exchange(conn.(*stream), 501, 128); err != nil {
							t.Fatal(err)
						}
						synctest.Wait()
						if !tr.joined.Load() || ledger.allocated != 1 {
							t.Fatalf("joined=%v allocations=%d want true/1", tr.joined.Load(), ledger.allocated)
						}
						delay := tr.retried.Sub(tr.completed)
						if delay < 500*time.Millisecond || delay > time.Second {
							t.Errorf("retry after joined completion=%v want jitter 0.5..1s", delay)
						}
						if err = client.Login(t.Context()); err != nil {
							t.Errorf("later safe owner control=%v", err)
						}
					})
				})
			}
		}
	}
}

var _ io.ReadCloser = (*accountDeadlineBody)(nil)

func TestAccountDefaultDeadlineBlockingControls(t *testing.T) {
	for _, startup := range []bool{false, true} {
		for _, path := range []string{"/v1/user/login", "/v1/user/current-profile"} {
			for _, kind := range []string{"malformed", "denied", "fatal"} {
				name := path + "/" + kind
				if startup {
					name = "startup-auto-select" + name
				} else {
					name = "eager" + name
				}
				t.Run(name, func(t *testing.T) {
					r5Credentials(t)
					synctest.Test(t, func(t *testing.T) {
						ledger := &r5Cloud{stops: map[string]int{}}
						body := r5Body(path, 0)
						if kind == "malformed" {
							body = `{"code":"invalid","data":null}`
							if path == "/v1/user/login" {
								body = `{"code":0,"data":{"accessToken":123}}`
							}
						}
						tr := &accountDeadlineControl{t: t, cloud: ledger, path: path, body: body, panicClose: kind == "fatal"}
						if kind == "denied" {
							tr.status = 403
						}
						client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: tr, StateDir: t.TempDir()})
						if err != nil {
							t.Fatal(err)
						}
						defer client.Close()
						if startup {
							_, err = selectDevice(t.Context(), client, cloudhttp.Device{}, nil, zeroRetryRNG{})
							if err == nil || errors.Is(err, cloud.ErrAccountUncertain) || cloud.Disposition(err) < cloud.Malformed {
								t.Fatalf("startup blocking classification=%v", err)
							}
						} else {
							s := r5Session(t, t.Context(), client, ledger)
							defer s.Close()
							<-time.After(90 * time.Second)
							synctest.Wait()
							ctx, cancel := context.WithTimeout(t.Context(), time.Second)
							defer cancel()
							conn, err := s.DialPort(ctx, 4700)
							if conn != nil {
								conn.Close()
							}
							if err == nil {
								t.Fatal("blocking completion admitted a client")
							}
						}
						err = client.Login(t.Context())
						if err == nil || errors.Is(err, cloud.ErrAccountUncertain) || cloud.Disposition(err) < cloud.Malformed {
							t.Fatalf("retained classification=%v", err)
						}
						synctest.Wait()
						if !tr.joined.Load() || ledger.allocated != 0 || tr.calls.Load() != 1 {
							t.Fatalf("joined=%v allocations=%d account calls=%d want true/0/1", tr.joined.Load(), ledger.allocated, tr.calls.Load())
						}
					})
				})
			}
		}
	}
}

func TestAccountPendingDeadlineRecovery(t *testing.T) {
	r5Credentials(t)
	for _, blocked := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			ledger := &r5Cloud{stops: map[string]int{}}
			body := "no response"
			if blocked {
				body = `{"code":0,"data":{"accessToken":123}}`
			}
			tr := &accountDeadlineControl{t: t, cloud: ledger, path: "/v1/user/login", body: body}
			client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: tr, StateDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			s := r5Session(t, t.Context(), client, ledger)
			defer s.Close()
			<-time.After(30*time.Second + 10*time.Millisecond)
			synctest.Wait()
			<-time.After(3 * time.Second)
			synctest.Wait()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			conn, err := s.DialPort(ctx, 4700)
			if conn != nil {
				conn.Close()
			}
			if blocked {
				if err == nil || ledger.allocated != 0 || tr.calls.Load() != 1 {
					t.Fatalf("canceled pending malformed result allowed acquisition: %v/%d/%d", err, ledger.allocated, tr.calls.Load())
				}
			} else if err != nil || ledger.allocated != 1 {
				t.Fatalf("canceled pending network result blocked recovery: %v/%d", err, ledger.allocated)
			}
		})
	}
}

func TestAllocatorJoinsCanceledAccountCompletion(t *testing.T) {
	r5Credentials(t)
	synctest.Test(t, func(t *testing.T) {
		ledger := &r5Cloud{stops: map[string]int{}, accountStall: "/v1/user/login"}
		client, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{Transport: ledger, StateDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		a := &allocator{c: client, d: cloudhttp.Device{Serial: "sim", Model: "S50 Pro"}}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		began := time.Now()
		lease, err := a.Allocate(ctx)
		if lease != nil || !errors.Is(err, session.ErrTransport) || errors.Is(err, session.ErrUncertain) || time.Since(began) != time.Second+20*time.Millisecond {
			t.Fatalf("canceled allocator result=%v/%v elapsed=%v want joined transport at 1.02s", lease, err, time.Since(began))
		}
		lease, err = a.Allocate(t.Context())
		if err != nil || lease == nil {
			t.Fatalf("safe completion cannot recover: %v/%v", lease, err)
		}
		if err = lease.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}
