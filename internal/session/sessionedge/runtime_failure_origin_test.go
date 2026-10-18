package sessionedge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
)

func TestAttackRuntimeNoticeOriginBothCases(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(fmt.Sprint("closed=", closed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var logs bytes.Buffer
				f := newFake()
				f.notices[session.DTLS] = make(chan session.Event, 1)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				sink := &failureObserver{ChannelSink: make(ChannelSink, 16), logger: slog.New(slog.NewJSONHandler(&logs, nil))}
				done := make(chan error, 1)
				go func() { done <- Run(ctx, session.DefaultConfig(), rng{}, f, nil, sink) }()
				synctest.Wait()
				if closed {
					close(f.notices[session.DTLS])
				} else {
					f.notices[session.DTLS] <- session.Event{Kind: session.Failed, Err: session.ErrTimeout}
				}
				synctest.Wait()
				cancel()
				<-done
				if len(sink.failed) != 1 {
					t.Fatalf("closed=%v failures=%d", closed, len(sink.failed))
				}
				var terminal *diagnostic.Terminal
				if !errors.As(sink.failed[0].Err, &terminal) {
					t.Errorf("closed=%v runtime DTLS notification lacks typed origin: %v", closed, sink.failed[0].Err)
				}
				wantReason := "timeout"
				if closed {
					wantReason = "transport_failed"
				}
				if p, r := diagnostic.Fields(sink.failed[0].Err, ""); p != "dtls" || r != wantReason {
					t.Errorf("closed=%v terminal=%s/%s, want dtls/%s", closed, p, r, wantReason)
				}
				if !strings.Contains(logs.String(), `"phase":"dtls"`) {
					t.Errorf("closed=%v runtime origin logged wrong: %s", closed, &logs)
				}
			})
		})
	}
}
