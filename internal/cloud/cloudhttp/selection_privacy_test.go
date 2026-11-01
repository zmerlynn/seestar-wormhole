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
		if err.Error() != "[device selection required]" {
			t.Fatal("unsafe Error text")
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
					assertSafeSelectionProjection(t, fmt.Sprintf(format, value))
				})
			}
			for _, json := range []bool{false, true} {
				name := "text"
				if json {
					name = "JSON"
				}
				t.Run(name, func(t *testing.T) {
					var output bytes.Buffer
					var handler slog.Handler = slog.NewTextHandler(&output, nil)
					if json {
						handler = slog.NewJSONHandler(&output, nil)
					}
					slog.New(handler).Error("synthetic failure", "err", value)
					assertSafeSelectionProjection(t, output.String())
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
