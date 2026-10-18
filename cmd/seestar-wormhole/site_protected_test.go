package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

func TestSiteProtectionRetiredFlags(t *testing.T) {
	for _, name := range []string{"site-location", "site-timezone", "app-overrides-site"} {
		_, err := parse([]string{"proxy", "--" + name + "=value"}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "supported options") {
			t.Fatalf("flag %s: got %v, want unknown flag", name, err)
		}
	}
}

func TestSiteProtectionStartupNoRequests(t *testing.T) {
	for _, flags := range [][]string{nil, {"--idle=2m"}} {
		t.Run(strings.Join(flags, ""), func(t *testing.T) {
			s := &siteCommandSession{idleCommandSession: &idleCommandSession{make(chan struct{})}, finished: make(chan struct{})}
			close(s.finished)
			var logs bytes.Buffer
			err := runWithListener(t.Context(), append([]string{"proxy"}, flags...), io.Discard, &logs, func(context.Context, proxy.Config) (commandSession, error) { return s, nil },
				func(string, string) (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") })
			if err != nil || s.calls.Load() != 0 {
				t.Fatalf("startup err=%v dials=%d logs=%s; want no device dials", err, s.calls.Load(), &logs)
			}
		})
	}
}

type siteCommandSession struct {
	*idleCommandSession
	finished chan struct{}
	calls    atomic.Int32
}

func (s *siteCommandSession) Done() <-chan struct{} { return s.finished }
func (s *siteCommandSession) DialPort(context.Context, uint16) (net.Conn, error) {
	s.calls.Add(1)
	return nil, errors.New("unexpected startup dial")
}
