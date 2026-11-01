package guitest

import (
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Read actual workflow invocations and the harness's actual flag default.
// Asking for help exits during flag parsing, before any native process starts.
func TestWorkflowTelescopeCampaigns(t *testing.T) {
	help := exec.CommandContext(t.Context(), "go", "run", "-tags=guitest", "./scripts/gui-autotest", "-h")
	help.Dir = "../.."
	output, err := help.CombinedOutput()
	if err != nil {
		t.Fatalf("harness help: %v\n%s", err, output)
	}
	defaults := regexp.MustCompile(`-scenarios string\n[^\n]*\(default "([^"]+)"\)`).FindSubmatch(output)
	if len(defaults) != 2 {
		t.Fatalf("scenario default absent from help: %s", output)
	}
	data, err := os.ReadFile("../../.github/workflows/gui-autotest.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct{ Steps []struct{ If, Run string } }
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	outFlag := regexp.MustCompile(`-out\s+"[^"]*[/\\](light|dark)"`)
	scenariosFlag := regexp.MustCompile(`-scenarios\s+([a-z0-9,-]+)`)
	for _, os := range []string{"Windows", "macOS", "Linux"} {
		campaigns := map[string][]string{}
		counts := map[string]int{}
		for _, step := range workflow.Jobs["native"].Steps {
			if step.If != "runner.os == '"+os+"'" {
				continue
			}
			// Join the workflow's shell line continuations before inspecting flags.
			for _, line := range strings.Split(strings.ReplaceAll(step.Run, "\\\n", " "), "\n") {
				if !strings.Contains(line, "gui-harness") {
					continue
				}
				out := outFlag.FindStringSubmatch(line)
				if len(out) != 2 {
					continue
				}
				list := string(defaults[1])
				if flag := scenariosFlag.FindStringSubmatch(line); len(flag) == 2 {
					list = flag[1]
				}
				campaigns[out[1]] = strings.Split(list, ",")
				counts[out[1]]++
			}
		}
		for theme, scenario := range map[string]string{"light": "telescope-picker", "dark": "dark-telescope-picker"} {
			t.Run(os+"/"+theme, func(t *testing.T) {
				if counts[theme] != 1 {
					t.Fatalf("expected one %s campaign, found %d", theme, counts[theme])
				}
				if !slices.Contains(campaigns[theme], scenario) {
					t.Fatalf("%s campaign omits %s", theme, scenario)
				}
			})
		}
	}
}
