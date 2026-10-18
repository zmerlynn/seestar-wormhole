package proxy

import (
	"bytes"
	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
	"log/slog"
	"strings"
	"testing"
)

func TestIOTCLayoutDebugBudget(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		var b bytes.Buffer
		x := &stack{f: &factory{s: &Session{logger: slog.New(slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: level}))}}}
		for range 100 {
			x.iotcAction(iotc.Action{Kind: iotc.ReceiveOddity, Opcode: 0x100c, Reason: "secret-sentinel", Data: []byte("secret-sentinel")})
		}
		if len(x.effects) != 0 {
			t.Fatal("diagnostic sent to protocol worker")
		}
		if level == slog.LevelInfo {
			if b.Len() != 0 {
				t.Fatal("layout oddity logged above debug")
			}
			continue
		}
		lines := strings.Split(strings.TrimSpace(b.String()), "\n")
		if len(lines) != 65 {
			t.Fatalf("records=%d want64 DEBUG plus1 WARN", len(lines))
		}
		if !strings.Contains(lines[64], `"level":"WARN"`) || !strings.Contains(lines[64], `"rule":"telemetry_detail_cap"`) {
			t.Fatalf("missing cap summary: %s", lines[64])
		}
		for _, line := range lines[:64] {
			var event struct {
				Message string `json:"msg"`
				Level   string `json:"level"`
				Opcode  int    `json:"opcode"`
			}
			if err := json.Unmarshal([]byte(line), &event, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatal(err)
			}
			if event.Message != "iotc_receive_oddity" || event.Level != "DEBUG" || event.Opcode != 0x100c || strings.Contains(line, "secret") || strings.Contains(line, "reason") || strings.Contains(line, "data") {
				t.Fatalf("unexpected layout log: %s", line)
			}
		}
	}
}
