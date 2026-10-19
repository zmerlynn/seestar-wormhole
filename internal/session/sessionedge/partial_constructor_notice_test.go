package sessionedge

import (
	"bytes"
	"context"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
)

func TestPartialConstructorNoticeReportedOnce(t *testing.T) {
	for _, typed := range []bool{false, true} {
		t.Run(fmt.Sprint(typed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFake()
				entered := make(chan struct{})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var cause error = session.ErrTimeout
				if typed {
					cause = diagnostic.NewTerminal(session.DTLS, "timeout", session.ErrTransport)
				}
				factory := factoryFunc(func(a uint64, b session.Budgets) (Layers, error) {
					l, _ := f.New(a, b)
					l.DTLS = reviewHandshake(func(c context.Context, _ Transport) (Transport, error) {
						close(entered)
						<-c.Done()
						tr, _ := f.transport(c, session.DTLS)
						return closeQueue(tr, []error{cause}), cause
					})
					return l, nil
				})
				var logs bytes.Buffer
				sink := &failureObserver{ChannelSink: make(ChannelSink, 16), logger: slog.New(slog.NewJSONHandler(&logs, nil))}
				done := make(chan error, 1)
				go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, factory, nil, sink) }()
				<-entered
				cancel()
				err := <-done
				if p, r := diagnostic.Fields(err, ""); p != "dtls" || r != "timeout" {
					t.Errorf("cause=%s/%s want dtls/timeout", p, r)
				}
				if n := strings.Count(logs.String(), `"level":"ERROR"`); n != 1 {
					t.Errorf("one constructor failure reported %d times typed=%v: %s", n, typed, &logs)
				}
				if f.allocated.Load() != 1 || f.stopped.Load() != 1 {
					t.Fatal("ownership changed")
				}
			})
		})
	}
}
