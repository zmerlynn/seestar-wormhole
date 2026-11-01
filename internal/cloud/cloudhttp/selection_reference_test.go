package cloudhttp

import (
	"os"
	"strings"
	"testing"
)

func TestCatalogModeReferenceUncertainty(t *testing.T) {
	data, err := os.ReadFile("../../../docs/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(string(data)), " ")
	t.Run("no assigned meaning", func(t *testing.T) {
		if strings.Contains(text, "`isContainShared=3` (all)") {
			t.Fatal("reference assigns an unknown filtering meaning")
		}
	})
	for _, phrase := range []string{
		"not evidence that mode 3 adds shared or rental devices",
		"server meaning of mode 3 and the omitted parameter's default remain unknown",
	} {
		t.Run(phrase, func(t *testing.T) {
			if !strings.Contains(text, phrase) {
				t.Fatal("reference lost the request provenance limit")
			}
		})
	}
}

func TestDeviceSelectionRefusedReference(t *testing.T) {
	data, err := os.ReadFile("../../../docs/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	const row = "| `device_selection_refused` | WARN | `rule=model_mismatch`; `requested_model` and `catalog_model` contain cloud model text with Go ASCII escaping, capped at 64 display characters including any truncation marker |"
	if !strings.Contains(string(data), row) {
		t.Fatal("device_selection_refused reference does not describe the escaped cloud model fields and display bound")
	}
}
