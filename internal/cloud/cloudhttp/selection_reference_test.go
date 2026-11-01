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
