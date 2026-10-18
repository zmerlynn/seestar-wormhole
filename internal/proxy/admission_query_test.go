package proxy

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"syscall"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

type queryFailureConn struct {
	net.Conn
	err error
}

func (c *queryFailureConn) socketError() error { return c.err }

type queryFailureDial struct {
	remote net.Conn
	done   chan struct{}
}

func (d queryFailureDial) DialPort(ctx context.Context, _ uint16) (net.Conn, error) {
	defer close(d.done)
	<-ctx.Done()
	if d.remote != nil {
		return d.remote, nil // Completion raced the admission cancellation.
	}
	return nil, ctx.Err()
}

func TestAdmissionSocketQueryDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		err          error
		query        bool
	}{
		{"invalid_query", "operation_failed", syscall.EINVAL, true},
		{"unsupported_query", "operation_failed", syscall.EOPNOTSUPP, true},
		{"short_query", "operation_failed", io.ErrUnexpectedEOF, true},
		{"query_permission", "permission_denied", syscall.EACCES, true},
		{"peer_reset", "connection_reset", syscall.ECONNRESET, false},
	} {
		for _, complete := range []bool{false, true} {
			outcome := "dial_canceled"
			if complete {
				outcome = "dial_completed"
			}
			t.Run(tc.name+"/"+outcome, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					a, b := net.Pipe()
					defer b.Close()
					err := tc.err
					if tc.query {
						err = diagnostic.Wrap("tcp_state_query", "", err)
					}
					l := &e2eSingleListener{conn: &queryFailureConn{a, err}, done: make(chan struct{})}
					d := queryFailureDial{done: make(chan struct{})}
					var peer net.Conn
					if complete {
						d.remote, peer = net.Pipe()
						defer peer.Close()
					}
					var logs bytes.Buffer
					logger := slog.New(slog.NewJSONHandler(&logs, nil))
					finished := make(chan error, 1)
					go func() { finished <- Serve(ctx, d, []Forward{{Listener: l, Port: 445}}, 1, logger) }()
					<-d.done
					synctest.Wait()
					// A refused client must not stop Serve or enter forwarding.
					select {
					case err := <-finished:
						t.Fatalf("Serve stopped after refusal: %v", err)
					default:
					}
					if _, err := b.Read(make([]byte, 1)); err != io.EOF {
						t.Fatalf("local connection not closed: %v", err)
					}
					if peer != nil {
						if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
							t.Fatalf("racing remote connection not closed: %v", err)
						}
					}
					cancel()
					if err := <-finished; err != nil {
						t.Fatal(err)
					}
					want := `"level":"INFO","msg":"client_canceled","port":445,"local_port":0,"client_class":"other","reason":"connection_reset"`
					if tc.query {
						want = `"level":"WARN","msg":"client_rejected","port":445,"local_port":0,"client_class":"other","rule":"SIO_TCP_INFO_required","reason":"` + tc.reason + `"`
					}
					if bytes.Count(logs.Bytes(), []byte("\n")) != 1 || !bytes.Contains(logs.Bytes(), []byte(want)) {
						t.Fatalf("want one diagnostic containing %s, got %s", want, logs.String())
					}
					t.Logf("socket diagnostic: %s", logs.String())
				})
			})
		}
	}
}
