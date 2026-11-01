package cloudhttp

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSelectionErrorGenericProjections(t *testing.T) {
	var logs bytes.Buffer
	c := selectionClient(t, &selectionCloud{bodies: []string{twoDevices}}, &logs, slog.LevelDebug)
	_, err := c.SelectDevice(t.Context(), Device{})
	if err == nil {
		t.Fatal("fixture did not refuse selection")
	}
	t.Run("Error", func(t *testing.T) {
		if err.Error() != "device_selection: multiple devices on account; set --serial" {
			t.Fatal("Error lost the sanitized selection reason")
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
					assertSafeSelectionProjection(t, text)
					if name == "direct" && text != "device_selection: multiple devices on account; set --serial" {
						t.Fatal("generic formatting lost the sanitized selection reason")
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
					if name == "direct" && !strings.Contains(output.String(), "device_selection: multiple devices on account; set --serial") {
						t.Fatal("slog lost the sanitized selection reason")
					}
				})
			}
		})
	}
}

func assertSafeSelectionProjection(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{"serial-sentinel-secret-1234", "other-serial-sentinel-5678", "S50 Pro", "S30"} {
		if strings.Contains(text, secret) {
			t.Fatal("account identity escaped generic error projection")
		}
	}
}
