//go:build gui && guitest

package ui

import (
	"strconv"
	"strings"
	"testing"
)

func TestCatalogDriverProbe(t *testing.T) {
	const message = `Account telescopes: Seestar S50 Pro synthetic-choice-1234, Future telescope\tmodel synthetic-choice-5678`
	for name, value := range map[string]string{
		"valid":      message,
		"missing":    "Connected to your Seestar",
		"serial":     strings.Replace(message, "synthetic-choice-1234", "***1234", 1),
		"model":      strings.Replace(message, "Seestar S50 Pro", "wrong", 1),
		"escaping":   strings.Replace(message, `\t`, "\t", 1),
		"incomplete": strings.Replace(message, "Account telescopes:", "Account telescopes (list may be incomplete):", 1),
	} {
		t.Run(name, func(t *testing.T) {
			script := `(async()=>{const el=id=>$(id),check=ok=>!!ok; $('events').textContent=` + strconv.Quote(value) + `; if ((` + guiTestCatalogEventProbe + `) !== ` + strconv.FormatBool(name == "valid") + `) throw new Error('catalog Recent events assertion accepted corruption or rejected valid text');})()`
			runPageRegression(t, "picker-ux-test.js", script)
		})
	}
}
