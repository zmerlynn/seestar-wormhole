package cloudhttp

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestExplicitModelRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, serial, model string
		refused             bool
		incomplete          bool
	}{
		{"serial mismatch", "serial-sentinel-secret-1234", "S30", true, false},
		{"default mismatch", "", "S30", true, false},
		{"serial equal", "serial-sentinel-secret-1234", "S50 Pro", false, false},
		{"default equal", "", "S50 Pro", false, false},
		{"omitted", "", "", false, false},
		{"incomplete mismatch", "serial-sentinel-secret-1234", "S30", true, true},
		{"incomplete equal", "serial-sentinel-secret-1234", "S50 Pro", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			body := oneDevice
			if tc.incomplete {
				body = strings.Replace(body, `"total":1`, `"total":0`, 1)
			}
			c := selectionClient(t, &selectionCloud{bodies: []string{body}}, &logs, slog.LevelWarn)
			got, err := c.SelectDevice(t.Context(), Device{Serial: tc.serial, Model: tc.model})
			if tc.refused {
				if !errors.Is(err, cloud.ErrConfig) || !strings.Contains(err.Error(), "S30") || !strings.Contains(err.Error(), "S50 Pro") || !strings.Contains(err.Error(), "--model") || got != (Device{}) {
					t.Fatalf("mismatch: device=%v error=%v", got, err)
				}
				for _, want := range []string{`"level":"WARN"`, `"rule":"model_mismatch"`} {
					if !strings.Contains(logs.String(), want) {
						t.Fatalf("missing %s: %s", want, &logs)
					}
				}
			} else if err != nil || got.Model != "S50 Pro" || logs.Len() != 0 {
				t.Fatalf("equal/omitted: device=%v error=%v logs=%s", got, err, &logs)
			}
			assertSelectionSecrets(t, logs.String())
		})
	}
}
