package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runShellRegression executes shell.js on its DOM double, seeded from
// shell.html, then the check named by call from shell-test.js.
func runShellRegression(t *testing.T, call string) {
	t.Helper()
	shell, err := exec.LookPath("jsc")
	if err != nil {
		t.Skip("JavaScriptCore shell is not installed")
	}
	var parts []string
	for _, name := range []string{"testdata/shell-dom.js", "", "web/shell.js", "testdata/shell-test.js"} {
		var b []byte
		switch {
		case name == "":
			b = []byte(pageDOMOf(t, "web/shell.html"))
		case strings.HasPrefix(name, "web/"):
			b, err = assets.ReadFile(name)
		default:
			b, err = os.ReadFile(name)
		}
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, string(b))
	}
	script := strings.Join(parts, "\n") + "\n" + call + `.then(() => print('PAGE REGRESSION PASS')).catch(e => {print(String(e)); quit(1);});`
	path := filepath.Join(t.TempDir(), "shell.js")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(t.Context(), shell, path).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "PAGE REGRESSION PASS") {
		t.Fatalf("shell regression failed or did not complete: %v\n%s", err, output)
	}
}

// The tab bar: a tab per open profile in order, each with its frame,
// selection kept across polls, an activation's pick, and no + for a
// --state-dir folder.
func TestShellTabBar(t *testing.T) { runShellRegression(t, "checkTabBar()") }

// Each tab's dot is its page's header state.
func TestShellDots(t *testing.T) { runShellRegression(t, "checkDots()") }

// The + menu offers the closed profiles and New profile…, with the
// refusals under the list or the name.
func TestShellMenu(t *testing.T) { runShellRegression(t, "checkMenu()") }

// Closing a copying or connected tab confirms first; the last tab's close
// quits.
func TestShellClose(t *testing.T) { runShellRegression(t, "checkClose()") }

// Quitting confirms once while any tab copies.
func TestShellQuit(t *testing.T) { runShellRegression(t, "checkQuit()") }

// Ctrl+Tab and Ctrl+Shift+Tab switch tabs, from the shell or a page, and
// the tab list's arrows move within it.
func TestShellSwitchKeys(t *testing.T) { runShellRegression(t, "checkSwitchKeys()") }

// The selected tab's page takes focus as it loads and as tabs switch,
// unless something in the shell has focus.
func TestShellFrameFocus(t *testing.T) { runShellRegression(t, "checkFrameFocus()") }

// WKWebView's own focus on the first tab at launch goes to the page, until
// the user acts in the shell.
func TestShellLaunchFocus(t *testing.T) { runShellRegression(t, "checkLaunchFocus()") }
