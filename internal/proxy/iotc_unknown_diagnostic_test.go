package proxy

import (
	"bytes"
	"github.com/go-json-experiment/json"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
)

func TestIOTCUnknownDiagnosticBudgetAndPrivacy(t *testing.T) {
	categories := []iotc.Oddity{iotc.UnknownRecord, iotc.UnknownControl, iotc.OpaqueRoute, iotc.AlertTrigger, iotc.UnknownAlert, iotc.UnknownRelayResult, iotc.UnknownMasterUnit, 255}
	wantNames := []string{"unknown_record", "unknown_control", "opaque_route", "alert_trigger", "unknown_alert", "unknown_relay_result", "unknown_master_unit", "other"}
	for _, interval := range []time.Duration{0, time.Second} {
		for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
			var b bytes.Buffer
			x := telemetryStack(t, interval, &b)
			x.f.s.logger = slog.New(slog.NewJSONHandler(&b, &slog.HandlerOptions{Level: level}))
			for n := range 100 {
				x.iotcAction(iotc.Action{Kind: iotc.ReceiveOddity, Oddity: categories[n%len(categories)], Type: 99, Opcode: 0xffff,
					Reason: "reason-secret", Domain: "name-secret", Data: []byte("payload-secret"), AAD: []byte("aad-secret"), Key: 0xdeadbeef, Private: 0xcafebabe,
					Link: iotc.Link{Address: netip.MustParseAddrPort("192.0.2.123:4567")}})
			}
			if len(x.effects) != 0 {
				t.Fatal("oddity reached protocol worker")
			}
			if level == slog.LevelInfo {
				if b.Len() != 0 {
					t.Fatal("oddity above debug")
				}
				continue
			}
			lines := strings.Split(strings.TrimSpace(b.String()), "\n")
			if len(lines) != 64 {
				t.Fatalf("records=%d want=64", len(lines))
			}
			for i, line := range lines {
				var event struct {
					Message  string `json:"msg"`
					Level    string `json:"level"`
					Category string `json:"category"`
					Opcode   int    `json:"opcode"`
					Type     int    `json:"type"`
				}
				if err := json.Unmarshal([]byte(line), &event, json.MatchCaseInsensitiveNames(true)); err != nil {
					t.Fatal(err)
				}
				if event.Message != "iotc_receive_oddity" || event.Level != "DEBUG" || event.Category != wantNames[i%len(wantNames)] || event.Opcode != 0xffff || event.Type != 99 {
					t.Fatalf("wrong oddity projection: %s", line)
				}
				for _, needle := range []string{"secret", "192.0.2", "deadbeef", "3735928559", "cafebabe", "3405691582", "reason", "domain", "payload", "aad", "link", "key", "private"} {
					if strings.Contains(line, needle) {
						t.Fatalf("unexpected diagnostic field %s", needle)
					}
				}
			}
		}
	}
}
