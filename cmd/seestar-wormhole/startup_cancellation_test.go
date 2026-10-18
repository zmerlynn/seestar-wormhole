package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"io"
	"strings"
	"testing"
)

func TestStartupOperatorCancelFinalRecord(t *testing.T) {
	var logs bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	newSession := func(c context.Context, _ proxy.Config) (commandSession, error) {
		cancel()
		<-c.Done()
		return nil, c.Err()
	}
	err := runWithSession(ctx, append([]string{"proxy"}, []string{"--bind", "127.0.0.1", "--port", "14779:445"}...), io.Discard, &logs, keylog.Open, newSession)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("unexpected result=%v", err)
	}
	// Startup cancellation already returns a nonzero command outcome. The
	// final-failure contract covers that exit too; steady-state orderly
	// cancellation returns nil and has no failure record.
	if strings.Count(logs.String(), `"msg":"proxy failed"`) != 1 || !strings.Contains(logs.String(), `"reason":"canceled","cleanup_reason":"none"`) {
		t.Errorf("startup cancellation lacks its final outcome: %s", &logs)
	}
}
