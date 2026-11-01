package ui

import (
	"encoding/json/v2"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	script := string(dom) + "\n" + pageDOM(t) + "\n" + string(app) + "\n" + string(check) + "\n" +
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

func TestDevicePickerPage(t *testing.T) {
	runPageRegression(t, "device-picker-test.js", "checkDevicePicker()")
}

func TestPickerInitialStep(t *testing.T) {
	runPageRegression(t, "device-picker-test.js", "checkPickerInitialStep()")
}

func TestChangeTelescopePage(t *testing.T) {
	for _, uncertain := range []string{"false", "true"} {
		t.Run(uncertain, func(t *testing.T) {
			runPageRegression(t, "change-telescope-test.js", "checkChangeTelescope("+uncertain+")")
		})
	}
}

// Seed the DOM double from the actual HTML so descriptions, visibility, and
// control order are tested against markup rather than copied fixture values.
func pageDOM(t *testing.T) string {
	t.Helper()
	b, err := assets.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(strings.NewReader(string(b)))
	decoder.Strict = false
	decoder.AutoClose = xml.HTMLAutoClose
	decoder.Entity = xml.HTMLEntity
	type element struct {
		ID, Parent, Text string
		Attrs            map[string]string
	}
	var elements []element
	var stack []int
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			attrs := map[string]string{}
			for _, a := range token.Attr {
				attrs[a.Name.Local] = a.Value
			}
			index := -1
			if id := attrs["id"]; id != "" {
				parent := ""
				for i := len(stack) - 1; i >= 0; i-- {
					if stack[i] >= 0 {
						parent = elements[stack[i]].ID
						break
					}
				}
				index = len(elements)
				elements = append(elements, element{ID: id, Parent: parent, Attrs: attrs})
			}
			stack = append(stack, index)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			for _, index := range stack {
				if index >= 0 {
					elements[index].Text += string(token)
				}
			}
		}
	}
	encoded, err := json.Marshal(elements)
	if err != nil {
		t.Fatal(err)
	}
	return `for (const e of ` + string(encoded) + `) {
 const el=document.getElementById(e.ID);
 el.id=e.ID; el.attributes=e.Attrs; el.textContent=e.Text;
 el.hidden='hidden' in e.Attrs; el.disabled='disabled' in e.Attrs;
 el.value=e.Attrs.value || '';
 if(e.Parent) document.getElementById(e.Parent).append(el);
 }`
}

func TestPickerStatusWrites(t *testing.T) {
	runPageRegression(t, "picker-ux-test.js", "checkStatusWrites()")
}

func TestPickerLabels(t *testing.T) {
	runPageRegression(t, "picker-ux-test.js", "checkPickerLabels()")
}

func TestChangeTelescopeChoosingPage(t *testing.T) {
	for _, uncertain := range []string{"false", "true"} {
		t.Run(uncertain, func(t *testing.T) {
			runPageRegression(t, "change-telescope-test.js", "checkChangeTelescope("+uncertain+", true)")
		})
	}
}

func TestManualPickerPage(t *testing.T) {
	for _, scenario := range []string{"label", "order", "initial order", "empty status", "serial help", "validation", "optional model", "saved without serial", "running action"} {
		t.Run(scenario, func(t *testing.T) {
			runPageRegression(t, "picker-ux-test.js", "checkManualPicker("+strconv.Quote(scenario)+")")
		})
	}
}

func TestSavedChoicePage(t *testing.T) {
	for _, scenario := range []string{"label", "help", "received", "account"} {
		t.Run(scenario, func(t *testing.T) {
			runPageRegression(t, "picker-ux-test.js", "checkSavedChoice("+strconv.Quote(scenario)+")")
		})
	}
}

func TestChangeTelescopeGroupPage(t *testing.T) {
	runPageRegression(t, "picker-ux-test.js", "checkChangeGroup()")
}
