package cloudhttp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Waiting for the transport connection to close joins its idle diagnostic path
// without a sleep.
func TestTransportDoesNotLogResponseSecrets(t *testing.T) {
	for _, injected := range []bool{false, true} {
		t.Run(fmt.Sprintf("injected=%t", injected), func(t *testing.T) {

			var sink lockedLogBuffer
			oldSlog, oldLog, oldFlags := slog.Default(), log.Writer(), log.Flags()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&sink, nil)))
			log.SetOutput(&sink)
			defer func() { slog.SetDefault(oldSlog); log.SetOutput(oldLog); log.SetFlags(oldFlags) }()
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "stop") {
					io.WriteString(w, `{"code":200,"data":null,"msg":"ok"}`)
					return
				}
				conn, b, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				fmt.Fprintf(b, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nContent-Type: application/json\r\n\r\n%s%s", len(online), online, "token-secret password-secret allocation-secret")
				if err := b.Flush(); err != nil {
					t.Error(err)
				}
			}))
			defer s.Close()
			cfg := Config{BaseURL: s.URL}
			if injected {
				cfg.Transport = s.Client().Transport
			}
			c, err := newAuthenticatedFixture(t, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			tr := c.transport.(*http.Transport)
			tr.TLSClientConfig = s.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			closed := make(chan struct{}, 2)
			tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				return &closeObservedConn{Conn: conn, closed: closed}, nil
			}
			lease, err := c.Acquire(t.Context(), Device{Serial: "serial", Model: "S50 Pro"})
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			select {
			case <-closed:
			case <-t.Context().Done():
				t.Fatal("transport did not close the allocation connection")
			}
			slog.Info("allocation", "lease", lease, "allocation", lease.Allocation())
			if got := sink.String(); strings.Contains(got, "secret") || strings.Contains(got, "opaque-license") {
				t.Errorf("standard logger or slog leaked response material: %s", got)
			}
		})
	}
}

type closeObservedConn struct {
	net.Conn
	once   sync.Once
	closed chan<- struct{}
}

func (c *closeObservedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.closed <- struct{}{} })
	return err
}

type lockedLogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}
