package cloudhttp

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Exercise net/http's own diagnostic boundaries, not only wrapped errors. Even
// unsolicited trailing bytes after a valid token must not reach the std logger.
func TestAccountTransportDiagnosticsNeverExposeSecrets(t *testing.T) {
	for _, injected := range []bool{false, true} {
		for _, mode := range []string{"unsolicited", "status", "header", "redirect"} {
			t.Run(fmt.Sprintf("%t/%s", injected, mode), func(t *testing.T) {
				var sink lockedLogBuffer
				old := log.Writer()
				log.SetOutput(&sink)
				defer log.SetOutput(old)
				t.Setenv("SEESTAR_EMAIL", "reader@example.invalid")
				t.Setenv("SEESTAR_PASSWORD", accountSentinel)
				s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "current-profile") {
						io.WriteString(w, `{"code":0,"msg":"ok","data":{"id":"user"}}`)
						return
					}
					conn, b, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					body := `{"code":0,"msg":"` + accountSentinel + `","data":{"accessToken":"` + accountSentinel + `"}}`
					switch mode {
					case "unsolicited":
						fmt.Fprintf(b, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s%s", len(body), body, accountSentinel)
					case "status":
						fmt.Fprintf(b, "HTTP/1.1 %s\r\n\r\n", accountSentinel)
					case "header":
						fmt.Fprintf(b, "HTTP/1.1 200 OK\r\n%s\x00: invalid\r\n\r\n", accountSentinel)
					case "redirect":
						fmt.Fprintf(b, "HTTP/1.1 307 %s\r\nLocation: https://example.invalid/%s\r\nContent-Length: 0\r\n\r\n", accountSentinel, accountSentinel)
					}
					if err := b.Flush(); err != nil {
						t.Error(err)
					}
				}))
				defer s.Close()
				cfg := Config{AccountBaseURL: s.URL, StateDir: t.TempDir()}
				if injected {
					cfg.Transport = s.Client().Transport
				}
				c, err := NewFromEnvironment(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				tr := c.transport.(*http.Transport)
				tr.TLSClientConfig = s.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
				closed := make(chan struct{}, 4)
				tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
					conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
					if err != nil {
						return nil, err
					}
					return &closeObservedConn{Conn: conn, closed: closed}, nil
				}
				err = c.Login(t.Context())
				if (err == nil) != (mode == "unsolicited") {
					t.Fatalf("unexpected result: %v", err)
				}
				<-closed
				if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err)+sink.String(), accountSentinel) {
					t.Fatal("HTTP diagnostics leaked secrets")
				}
			})
		}
	}
}
