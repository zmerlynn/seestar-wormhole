package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"net"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"github.com/zmerlynn/seestar-wormhole/internal/ui"
)

type stoppedUISession struct {
	*idleCommandSession
	outcome error
}

func (s *stoppedUISession) Close() error { s.idleCommandSession.Close(); return s.outcome }

func TestUIStopLogsLikeCLISteadyShutdown(t *testing.T) {
	for _, name := range []string{"clean", "cleanup failure"} {
		t.Run(name, func(t *testing.T) {
			cleanupFailure := name == "cleanup failure"
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var logs bytes.Buffer
			newSession := func(context.Context, proxy.Config, cloud.Credentials) (commandSession, error) {
				cancel() // Stop/Quit cancels the run's context before Close joins cleanup.
				outcome := error(context.Canceled)
				if cleanupFailure {
					outcome = errors.Join(outcome, session.ErrStop)
				}
				return &stoppedUISession{&idleCommandSession{done: make(chan struct{})}, outcome}, nil
			}
			runner := newUIRunner(t.TempDir(), &logs, newSession, func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }, func(context.Context) error { return nil })
			err := runner(ctx, ui.Settings{Version: 1, Bind: "127.0.0.1", Idle: "0"}, cloud.Credentials{}, proxy.NewStatus())
			if !errors.Is(err, context.Canceled) || errors.Is(err, session.ErrStop) != cleanupFailure {
				t.Fatalf("unexpected run result: %v", err)
			}
			cleanupRecords, errorRecords := 0, 0
			wantCleanup := "none"
			if cleanupFailure {
				wantCleanup = "release_failed"
			}
			for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte{'\n'}) {
				var record struct {
					Level, Msg    string
					CleanupReason string `json:"cleanup_reason"`
				}
				if err := json.Unmarshal(line, &record, json.MatchCaseInsensitiveNames(true)); err != nil {
					t.Fatal(err)
				}
				if record.Msg == "proxy_cleanup_complete" && record.Level == "INFO" && record.CleanupReason == wantCleanup {
					cleanupRecords++
				}
				if record.Level == "ERROR" {
					errorRecords++
					if record.Msg != "proxy failed" || record.CleanupReason != wantCleanup {
						t.Fatalf("unexpected ERROR record: %s", line)
					}
				}
			}
			wantErrors := 0
			if cleanupFailure {
				wantErrors = 1
			}
			if cleanupRecords != 1 || errorRecords != wantErrors {
				t.Fatalf("cleanup records=%d error records=%d: %s", cleanupRecords, errorRecords, &logs)
			}
		})
	}
}
