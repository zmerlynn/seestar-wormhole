package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"io"
	"strings"
	"testing"
)

type primaryCauseSession struct {
	*cleanupSession
	primary error
}

func (s *primaryCauseSession) Close() error { s.closes.Add(1); return s.primary }

func TestCommandPrimaryIsNotCleanupFailure(t *testing.T) {
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var s *primaryCauseSession
	newSession := func(_ context.Context, cfg proxy.Config) (commandSession, error) {
		primary := diagnostic.NewTerminal(session.RDT, "timeout", session.ErrTransport)
		diagnostic.Log(cfg.Logger, "layer_failed", "session", primary)
		done := make(chan struct{})
		close(done)
		s = &primaryCauseSession{&cleanupSession{done: done}, errors.Join(context.Canceled, primary)}
		cancel()
		return s, nil
	}
	err := runWithSession(ctx, append([]string{"proxy"}, []string{"--bind", "127.0.0.1", "--port", "14778:445"}...), io.Discard, &logs, newSession)
	if p, r := diagnostic.Fields(err, ""); p != "rdt" || r != "timeout" {
		t.Errorf("primary outcome=%s/%s", p, r)
	}
	if s.closes.Load() != 1 {
		t.Errorf("Close calls=%d", s.closes.Load())
	}
	if strings.Count(logs.String(), `"level":"ERROR"`) != 2 {
		t.Errorf("want owner diagnostic and final outcome: %s", &logs)
	}
	if !strings.Contains(logs.String(), `"msg":"proxy_cleanup_complete","cleanup_reason":"none"`) {
		t.Errorf("successful release falsely labeled failed cleanup: %s", &logs)
	}
}

func TestCommandPrimaryAndStopFailureAreReported(t *testing.T) {
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var s *primaryCauseSession
	newSession := func(_ context.Context, cfg proxy.Config) (commandSession, error) {
		primary := diagnostic.NewTerminal(session.RDT, "timeout", session.ErrTransport)
		diagnostic.Log(cfg.Logger, "layer_failed", "session", primary)
		done := make(chan struct{})
		close(done)
		s = &primaryCauseSession{&cleanupSession{done: done}, errors.Join(context.Canceled, primary, session.ErrStop)}
		cancel()
		return s, nil
	}
	err := runWithSession(ctx, append([]string{"proxy"}, []string{"--bind", "127.0.0.1", "--port", "14778:445"}...), io.Discard, &logs, newSession)
	if p, r := diagnostic.Fields(err, ""); p != "rdt" || r != "timeout" || !errors.Is(err, session.ErrStop) {
		t.Errorf("final owner outcome=%s/%s (%v), want retained primary and failed stop", p, r, err)
	}
	if s.closes.Load() != 1 {
		t.Errorf("Close calls=%d, want 1", s.closes.Load())
	}
	if strings.Count(logs.String(), `"level":"ERROR"`) != 2 || !strings.Contains(logs.String(), `"reason":"timeout","cleanup_reason":"release_failed"`) {
		t.Errorf("final outcome must retain primary and cleanup together: %s", &logs)
	}
	if !strings.Contains(logs.String(), `"msg":"proxy_cleanup_complete","cleanup_reason":"release_failed"`) {
		t.Errorf("failed cleanup status missing: %s", &logs)
	}
}
