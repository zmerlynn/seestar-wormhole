package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCatalogLogSurvivesSelectionRefusal(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		var output bytes.Buffer
		s := newStartupLogging(options{format: format, level: "info"}, &output)
		s.logger.Info("device_catalog", "devices", []map[string]string{{"model": "S30", "serial": "synthetic-catalog"}}, "count", 1, "complete", true)
		s.logger.Info("unrelated", "serial", "synthetic-other")
		if !strings.Contains(output.String(), "device_catalog") || !strings.Contains(output.String(), "synthetic-catalog") || strings.Contains(output.String(), "synthetic-other") {
			t.Fatal("catalog routing lost or widened")
		}
		s.finish(&argumentError{Option: "--serial", Rule: "synthetic"})
		if strings.Count(output.String(), "device_catalog") != 1 || strings.Contains(output.String(), "synthetic-other") {
			t.Fatal("refusal dropped/duplicated catalog or admitted other output")
		}
	}
}
