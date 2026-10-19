package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

func TestTCPStateStartupRefusal(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		for _, tc := range []struct {
			name, phase, reason string
			err                 error
		}{
			{"failed", "tcp_state_query", "operation_failed", syscall.EINVAL},
			{"short", "tcp_state_query", "operation_failed", io.ErrUnexpectedEOF},
			{"permission", "tcp_state_query", "permission_denied", syscall.EACCES},
			{"loopback", "tcp_state_loopback", "loopback_unavailable", syscall.EACCES},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				var out, logs bytes.Buffer
				checks := 0
				code := mainExitCodeWithRunner([]string{"proxy", "--log-format=" + format, "--log-level=error"}, &out, &logs,
					func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
					func() (io.Writer, func()) { return &logs, func() {} }, false,
					func(ctx context.Context, o options, _ io.Writer, stderr io.Writer) error {
						return runProxy(ctx, o, stderr,
							func(context.Context, proxy.Config) (commandSession, error) {
								t.Fatal("session opened before capability check")
								return nil, nil
							},
							func(string, string) (net.Listener, error) {
								t.Fatal("listener opened before capability check")
								return nil, nil
							},
							listenCommandDiscovery,
							func(context.Context) error { checks++; return diagnostic.Wrap(tc.phase, tc.reason, tc.err) })
					})
				if code != 1 || checks != 1 || out.Len() != 0 || strings.Count(logs.String(), "\n") != 1 {
					t.Fatalf("exit=%d checks=%d stdout=%q logs=%s", code, checks, &out, &logs)
				}
				var record map[string]any
				if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
					t.Fatal(err)
				}
				for key, want := range map[string]string{
					"msg": "proxy failed", "level": "ERROR", "phase": tc.phase,
					"reason": tc.reason, "cleanup_reason": "none", "rule": "SIO_TCP_INFO_required",
					"requirement": "Windows 10 version 1703 or later, or Windows Server 2016 or later",
				} {
					if record[key] != want {
						t.Errorf("%s=%v want %s: %s", key, record[key], want, &logs)
					}
				}
				t.Logf("startup record: %s", &logs)
			})
		}
	}
}
