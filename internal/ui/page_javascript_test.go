package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestChangeTelescopeAcrossStatusPoll(t *testing.T) {
	runPageRegression(t, "change-telescope-poll-test.js", "checkChangeTelescopeAcrossPoll()")
}

func TestChangeTelescopeRetryAfterReload(t *testing.T) {
	for _, pair := range []string{"cleared", "saved pair"} {
		t.Run(pair, func(t *testing.T) {
			call := "checkChangeTelescopeRetryAfterReload(false)"
			if pair == "saved pair" {
				call = "checkChangeTelescopeRetryAfterReload(true)"
			}
			runPageRegression(t, "change-telescope-reload-test.js", call)
		})
	}
}

// Execute the complete production script without a browser, HTTP, or keychain.
func runPageRegression(t *testing.T, file, call string) {
	t.Helper()
	shell, err := exec.LookPath("jsc")
	if err != nil {
		t.Skip("JavaScriptCore shell is not installed")
	}
	dom, err := os.ReadFile("testdata/device-picker-dom.js")
	if err != nil {
		t.Fatal(err)
	}
	app, err := assets.ReadFile("web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	check, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	script := string(dom) + "\n" + string(app) + "\n" + string(check) + "\n" +
		call + `.then(() => print('PAGE REGRESSION PASS')).catch(e => {print(String(e)); quit(1);});`
	path := filepath.Join(t.TempDir(), "page.js")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(t.Context(), shell, path).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "PAGE REGRESSION PASS") {
		t.Fatalf("page regression failed or did not complete: %v\n%s", err, output)
	}
}
