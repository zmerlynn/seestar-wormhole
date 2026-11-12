package cloudhttp

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSelectionErrorProjections(t *testing.T) {
	var logs bytes.Buffer
	c := selectionClient(t, &selectionCloud{bodies: []string{twoDevices}}, &logs, slog.LevelDebug)
	_, err := c.SelectDevice(t.Context(), Device{})
	if err == nil {
		t.Fatal("fixture did not refuse selection")
	}
	t.Run("Error", func(t *testing.T) {
		if err.Error() != wantSelection {
			t.Fatalf("Error=%q, want %q", err.Error(), wantSelection)
		}
	})
	joined := errors.Join(err)
	for name, value := range map[string]error{
		"direct": err, "joined": joined,
		"wrapped":   fmt.Errorf("startup: %w", joined),
		"formatted": errors.New(fmt.Sprintf("startup: %v", joined)),
	} {
		t.Run(name, func(t *testing.T) {
			for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
				t.Run(format, func(t *testing.T) {
					text := fmt.Sprintf(format, value)
					if format == "%#v" && name != "direct" {
						// Go syntax of the wrapper, not of the selection error.
						assertSelectionSecrets(t, text)
						return
					}
					assertSafeSelectionProjection(t, text)
					if name == "direct" && text != wantSelection {
						t.Fatalf("formatted %q, want %q", text, wantSelection)
					}
				})
			}
			for _, json := range []bool{false, true} {
				handlerName := "text"
				if json {
					handlerName = "JSON"
				}
				t.Run(handlerName, func(t *testing.T) {
					var output bytes.Buffer
					var handler slog.Handler = slog.NewTextHandler(&output, nil)
					if json {
						handler = slog.NewJSONHandler(&output, nil)
					}
					slog.New(handler).Error("synthetic failure", "err", value)
					assertSafeSelectionProjection(t, output.String())
					if name == "direct" && !strings.Contains(output.String(), "device_selection: multiple devices on account; set --serial; received telescopes: ") {
						t.Fatal("slog lost the selection reason")
					}
				})
			}
		})
	}
}

const wantSelection = "device_selection: multiple devices on account; set --serial; received telescopes: S50 Pro serial-sentinel-secret-1234, S30 other-serial-sentinel-5678"

// assertSafeSelectionProjection wants both received telescopes and nothing
// else from the account or its reply.
func assertSafeSelectionProjection(t *testing.T, text string) {
	t.Helper()
	for _, identity := range []string{"S50 Pro serial-sentinel-secret-1234", "S30 other-serial-sentinel-5678"} {
		if !strings.Contains(text, identity) {
			t.Fatalf("projection lost telescope %q: %s", identity, text)
		}
	}
	assertSelectionSecrets(t, text)
	for _, raw := range []string{"synthetic-wifi", "deviceSn", "deviceModel", "deviceStatus"} {
		if strings.Contains(text, raw) {
			t.Fatalf("cloud reply field %q escaped the projection", raw)
		}
	}
}

func TestSelectionErrorKeepsTheMessageBudget(t *testing.T) {
	var bodies []string
	for start := 0; start < 256; start += 100 {
		var items []string
		for i := start; i < min(start+100, 256); i++ {
			items = append(items, fmt.Sprintf(`{"deviceSn":"synthetic-serial-%04d-%s","deviceModel":"%s","deviceStatus":1,"ssid":"synthetic-wifi"}`, i, strings.Repeat("x", 40), strings.Repeat("m", 200)))
		}
		bodies = append(bodies, `{"code":200,"msg":"ok","data":{"total":256,"items":[`+strings.Join(items, ",")+`]}}`)
	}
	var logs bytes.Buffer
	c := selectionClient(t, &selectionCloud{bodies: bodies}, &logs, slog.LevelInfo)
	_, err := c.SelectDevice(t.Context(), Device{})
	var selection *SelectionError
	if !errors.As(err, &selection) {
		t.Fatalf("no selection error: %v", err)
	}
	text := err.Error()
	if len(text) > 4096 || !strings.Contains(text, "received telescopes: "+strings.Repeat("m", 61)+"... synthetic-serial-0000-") || !strings.Contains(text, " more") {
		t.Fatalf("unbounded or empty projection (%d bytes): %.200s", len(text), text)
	}
	assertSelectionSecrets(t, text)
}
