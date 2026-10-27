package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type stoppedUISession struct {
	*idleCommandSession
	outcome error
}

func (s *stoppedUISession) Close() error { s.idleCommandSession.Close(); return s.outcome }

func TestUIStopLogsLikeCLISteadyShutdown(t *testing.T) {
	for _, cleanupFailure := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		o, err := parse([]string{"proxy"}, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		o.command = commandUI
		var logs bytes.Buffer
		newSession := func(context.Context, proxy.Config) (commandSession, error) {
			cancel() // Stop/Quit cancels the run's context before Close joins cleanup.
			outcome := error(context.Canceled)
			if cleanupFailure {
				outcome = errors.Join(outcome, session.ErrStop)
			}
			return &stoppedUISession{&idleCommandSession{done: make(chan struct{})}, outcome}, nil
		}
		err = runProxy(ctx, o, &logs, newSession, func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }, listenCommandDiscovery, func(context.Context) error { return nil })
		cancel()
		if strings.Contains(logs.String(), `"msg":"proxy failed"`) != cleanupFailure {
			t.Fatalf("cleanupFailure=%v err=%v logs=%s", cleanupFailure, err, &logs)
		}
		if !strings.Contains(logs.String(), `"level":"INFO","msg":"proxy_cleanup_complete"`) {
			t.Fatalf("missing CLI cleanup log: %s", &logs)
		}
	}
}
