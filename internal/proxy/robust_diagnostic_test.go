package proxy

import (
	"bytes"
	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/rdt"
	"log/slog"
	"strings"
	"testing"
)

func TestReceiveOddityLogging(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		var b bytes.Buffer
		x := &stack{f: &factory{s: &Session{logger: slog.New(slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: level}))}}}
		x.rdtAction(rdt.Action{Kind: rdt.ReceiveOddity, Type: 0x44, Data: []byte("secret-sentinel"), Reason: "secret-sentinel"})
		x.tunnelAction(p2ptunnel.Action{Kind: p2ptunnel.ReceiveOddity, Command: 10, Channel: 7, Data: []byte("secret-sentinel"), Reason: "secret-sentinel"})
		if level == slog.LevelInfo {
			if b.Len() != 0 {
				t.Fatal("oddities logged above debug")
			}
			continue
		}
		lines := strings.Split(strings.TrimSpace(b.String()), "\n")
		if len(lines) != 2 {
			t.Fatalf("logs=%s", b.String())
		}
		for i, line := range lines {
			var event struct {
				Message string `json:"msg"`
				Level   string `json:"level"`
				Type    int    `json:"type"`
			}
			if err := json.Unmarshal([]byte(line), &event, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatal(err)
			}
			want := []int{0x44, 10}[i]
			if event.Level != "DEBUG" || event.Type != want || strings.Contains(line, "secret") || strings.Contains(line, "channel") || strings.Contains(line, "reason") || strings.Contains(line, "data") {
				t.Fatalf("unsafe diagnostic: %s", line)
			}
		}
	}
}
