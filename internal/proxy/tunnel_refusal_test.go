package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
)

func TestTunnelRefusalWarn(t *testing.T) {
	for _, rule := range []string{"R065", "R069"} {
		t.Run(rule, func(t *testing.T) {
			var logs bytes.Buffer
			x := &stack{f: &factory{s: &Session{logger: slog.New(slog.NewJSONHandler(&logs, nil))}}}
			c := newStream(x.f.s, 42, 80)
			x.channels[7] = c
			refusal := fmt.Errorf("%s: discarded 6 bytes", rule)
			x.tunnelAction(p2ptunnel.Action{Kind: p2ptunnel.CloseLocal, Channel: 7, Rule: rule, DiscardedBytes: 6, Err: refusal})
			if !errors.Is(c.terminal, refusal) || !strings.Contains(c.terminal.Error(), "discarded 6 bytes") {
				t.Fatalf("local error = %v", c.terminal)
			}
			if !strings.Contains(logs.String(), `"level":"WARN"`) || !strings.Contains(logs.String(), `"rule":"`+rule+`"`) || !strings.Contains(logs.String(), `"discarded_bytes":6`) || !strings.Contains(logs.String(), `"channel":7`) {
				t.Fatalf("log = %s", &logs)
			}
		})
	}
}
