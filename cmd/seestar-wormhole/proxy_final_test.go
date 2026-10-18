package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type finalFailureListener struct {
	done    chan struct{}
	once    sync.Once
	failure error
}

func (l *finalFailureListener) Accept() (net.Conn, error) {
	if l.failure != nil {
		return nil, l.failure
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *finalFailureListener) Close() error { l.once.Do(func() { close(l.done) }); return nil }
func (*finalFailureListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
}

type finalFailureSession struct {
	idleCommandSession
	outcome error
}

func (s *finalFailureSession) Close() error { return s.outcome }

func TestProxyFinalFailureRecord(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		for _, path := range []string{"parse", "trace", "bind", "configuration", "construction", "accept", "owner", "joined", "cleanup", "canceled", "orderly"} {
			t.Run(format+"/"+path, func(t *testing.T) {
				args := []string{"proxy", "--log-format=" + format, "--log-level=error"}
				wantReason, wantCleanup, wantCode := "timeout", "none", 1
				outcome := error(nil)
				switch path {
				case "parse":
					args = append(args, "--port=bad")
					wantReason, wantCode = "invalid_option", 2
				case "trace":
					args = append(args, "--trace="+filepath.Join(t.TempDir(), "missing", "file"))
					wantReason, wantCode = "invalid_option", 2
				case "bind", "configuration":
					wantReason, wantCode = "invalid_option", 2
				case "construction":
					wantReason = "cloud_license_missing"
				case "accept":
					wantReason = "operation_failed"
				case "owner", "joined":
					outcome = diagnostic.NewTerminal(session.RDT, "timeout", session.ErrTransport)
				case "cleanup":
					outcome = session.ErrStop
					wantReason, wantCleanup = "release_failed", "release_failed"
				case "canceled":
					outcome = context.Canceled
					wantReason = "canceled"
				case "orderly":
					wantCode = 0
				}
				var logs bytes.Buffer
				factory := func(_ context.Context, cfg proxy.Config) (commandSession, error) {
					switch path {
					case "configuration":
						return nil, &argumentError{Option: "--serial", Rule: "select an existing device from the account catalog"}
					case "construction":
						return nil, diagnostic.NewTerminal(session.Cloud, "cloud_license_missing", session.ErrTransport)
					case "owner", "joined":
						// Owner observation consumes the receipts. The command still owes one
						// final record, with its primary and final cleanup in the same object.
						if path == "joined" {
							outcome = errors.Join(outcome, diagnostic.NewTerminal(session.DTLS, "write_failed", session.ErrTransport), session.ErrStop)
							wantCleanup = "release_failed"
						}
						diagnostic.Log(cfg.Logger, "layer_failed", "session", outcome)
					}
					done := make(chan struct{})
					if path != "accept" {
						close(done)
					}
					return &finalFailureSession{idleCommandSession{done}, outcome}, nil
				}
				listen := func(string, string) (net.Listener, error) {
					if path == "bind" {
						return nil, errors.New("PRIVATE_BIND")
					}
					l := &finalFailureListener{done: make(chan struct{})}
					if path == "accept" {
						l.failure = errors.New("PRIVATE_ACCEPT")
					}
					return l, nil
				}
				err := runWithListener(t.Context(), args, io.Discard, &logs, keylog.Open, factory, listen)
				if got := commandExitCode(options{command: commandProxy}, err); got != wantCode {
					t.Fatalf("exit=%d want %d: %v", got, wantCode, err)
				}
				count := 0
				for _, line := range bytes.Split(logs.Bytes(), []byte{'\n'}) {
					if !bytes.HasPrefix(line, []byte{'{'}) {
						continue
					}
					var record map[string]any
					if err := json.Unmarshal(line, &record); err != nil {
						t.Fatal(err)
					}
					if record["msg"] != "proxy failed" {
						continue
					}
					count++
					if record["reason"] != wantReason || record["cleanup_reason"] != wantCleanup {
						t.Errorf("final=%v, want reason=%s cleanup_reason=%s", record, wantReason, wantCleanup)
					}
				}
				wantCount := 1
				if wantCode == 0 {
					wantCount = 0
				}
				if count != wantCount {
					t.Errorf("final records=%d want %d: %s", count, wantCount, &logs)
				}
				if strings.Contains(logs.String(), "PRIVATE_") {
					t.Errorf("private error leaked: %s", &logs)
				}
			})
		}
	}
}
