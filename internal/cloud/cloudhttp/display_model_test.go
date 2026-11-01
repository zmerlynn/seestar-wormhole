package cloudhttp

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestCatalogPublicModelNames(t *testing.T) {
	for _, model := range []string{"Seestar S50", "Seestar S50 Pro", "Seestar S30", "Seestar S30 Pro", "Seestar S30 Plus", "Seestar S80"} {
		t.Run(model, func(t *testing.T) {
			var logs bytes.Buffer
			f := &selectionCloud{bodies: []string{strings.Replace(oneDevice, "S50 Pro", model, 1)}}
			c := selectionClient(t, f, &logs, slog.LevelInfo)
			got, err := c.SelectDevice(t.Context(), Device{})
			if err != nil || got.Model != model {
				t.Fatalf("model=%q error=%v", got.Model, err)
			}
			if !strings.Contains(logs.String(), `"model":"`+model+`"`) {
				t.Fatalf("public model missing: %s", &logs)
			}
			if got.DisplayName() != model+" ***1234" {
				t.Fatalf("display=%q", got.DisplayName())
			}
			assertSelectionSecrets(t, logs.String()+got.DisplayName())
		})
	}
}
