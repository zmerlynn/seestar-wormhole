package ui

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestNativeSetupAssertionMatchesMarkup(t *testing.T) {
	source, err := os.ReadFile("testdata/page-state.js")
	if err != nil {
		t.Fatal(err)
	}
	// Execute the native driver's exact loop, including its descriptions and
	// placeholder expectations, against the production HTML projection.
	start := strings.Index(string(source), "      for (const [id, placeholder]")
	if start < 0 {
		t.Fatal("native setup loop missing")
	}
	end := strings.Index(string(source)[start:], "\n      }")
	if end < 0 {
		t.Fatal("native setup loop end missing")
	}
	loop := string(source)[start : start+end+len("\n      }")]
	runPageRegression(t, "native-setup-test.js", "checkNativeSetup("+strconv.Quote(loop)+")")
}
