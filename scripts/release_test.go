//go:build release

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestReleaseAssetNames(t *testing.T) {
	want := []string{
		"seestar-wormhole-cli_v0.5.0_darwin_amd64", "seestar-wormhole-cli_v0.5.0_darwin_arm64",
		"seestar-wormhole-cli_v0.5.0_linux_amd64", "seestar-wormhole-cli_v0.5.0_linux_arm64",
		"seestar-wormhole-cli_v0.5.0_windows_amd64.exe", "seestar-wormhole-cli_v0.5.0_windows_arm64.exe",
		"seestar-wormhole_v0.5.0_darwin_universal.dmg",
		"seestar-wormhole_v0.5.0_linux_amd64", "seestar-wormhole_v0.5.0_linux_arm64",
		"seestar-wormhole_v0.5.0_windows_amd64.exe", "seestar-wormhole_v0.5.0_windows_arm64.exe",
	}
	if got := expectedAssets("v0.5.0"); !slices.Equal(got, want) {
		t.Fatalf("assets=%v, want %v", got, want)
	}
	if got := assetName("v0.5.0-rc.1", "windows", "arm64", false); got != "seestar-wormhole-cli_v0.5.0-rc.1_windows_arm64.exe" {
		t.Fatal(got)
	}
}

func TestReleaseBareAssetsAndChecksums(t *testing.T) {
	t.Chdir(t.TempDir())
	builds := 0
	err := release("v0.5.0", "revision", func(version, revision, goos, arch string, gui bool, path string) error {
		builds++
		if gui && goos != "windows" {
			t.Fatal("container attempted native UI build")
		}
		return os.WriteFile(path, []byte("abc"), 0755)
	})
	if err != nil {
		t.Fatal(err)
	}
	if builds != 8 {
		t.Fatalf("builds=%d, want eight", builds)
	}
	check := func(want int) {
		t.Helper()
		data, err := os.ReadFile("dist/SHA256SUMS")
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		if len(lines) != want || !slices.IsSorted(lines) {
			t.Fatalf("checksums=%s", data)
		}
		for _, line := range lines {
			hash, name, ok := strings.Cut(line, "  ")
			// Literal known SHA-256 for abc: no checksum helper shared with production.
			if !ok || hash != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
				t.Fatalf("checksum line %q", line)
			}
			data, err := os.ReadFile(filepath.Join("dist", name))
			if err != nil || string(data) != "abc" {
				t.Fatalf("bare asset %s: %q %v", name, data, err)
			}
		}
	}
	check(8)
	if err := finalChecksums("v0.5.0"); err == nil {
		t.Fatal("accepted incomplete release")
	}
	for _, name := range expectedAssets("v0.5.0") {
		if err := os.WriteFile(filepath.Join("dist", name), []byte("abc"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := finalChecksums("v0.5.0"); err != nil {
		t.Fatal(err)
	}
	check(11)
	if err := os.WriteFile("dist/unexpected", nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := finalChecksums("v0.5.0"); err == nil {
		t.Fatal("accepted unexpected asset")
	}
}

func TestReleaseDoesNotPublishFailedBuilds(t *testing.T) {
	t.Chdir(t.TempDir())
	err := release("v0.5.0", "revision", func(_, _, _, _ string, _ bool, path string) error {
		if strings.Contains(path, "arm64") {
			return errors.New("build failed")
		}
		return os.WriteFile(path, []byte("abc"), 0755)
	})
	if err == nil {
		t.Fatal("ignored failed build")
	}
	entries, err := os.ReadDir("dist")
	if err != nil || len(entries) != 0 {
		t.Fatalf("published incomplete output: %v %v", entries, err)
	}
}

func TestReleaseVersions(t *testing.T) {
	if _, err := os.Stat("scripts/release.sh"); os.IsNotExist(err) {
		t.Chdir("..")
	}
	script, err := filepath.Abs("scripts/release.sh")
	if err != nil {
		t.Fatal(err)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	// These stand-ins only let valid shell arguments reach the build boundary.
	// An invalid version must be refused without either git or Go being invoked.
	bin := t.TempDir()
	for _, name := range []string{"git", "go"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		version string
		valid   bool
	}{
		{"v0.1.0", true}, {"v1.0.0-rc.1", true}, {"v0.0.0", true},
		{"v12.345.678-rc.0", true}, {"v1.2.3-rc.123", true},
		{"v01.0.0", false}, {"v1.00.0", false}, {"v1.0.00", false},
		{"v1.0", false}, {"v1.0.0-rc.01", false}, {"v1.0.0-beta", false},
		{"1.0.0", false}, {"v1.0.0+meta", false}, {"v1.0.0-rc", false},
		{"v1.0.0-rc.-1", false}, {"v1.0.0-rc.1.2", false}, {"v1.0.0-RC.1", false},
		{"v1.0.0\n", false}, {" v1.0.0", false}, {"", false},
	} {
		t.Run(fmt.Sprintf("%q", tc.version), func(t *testing.T) {
			if got := releaseVersion.MatchString(tc.version); got != tc.valid {
				t.Errorf("Go validation=%t, want %t", got, tc.valid)
			}
			// Go's Windows quoting leaves LF unquoted; MSYS argv parsing then
			// strips it. Pass the literal value through the environment and let
			// Bash construct its quoted argument, as the release workflow does.
			cmd := exec.Command(bash, "-c", `exec "$BASH" "$1" "$RELEASE_TEST_VERSION"`, "release-test", script)
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "RELEASE_TEST_VERSION="+tc.version)
			output, err := cmd.CombinedOutput()
			want := 0
			if !tc.valid {
				want = 2
			}
			got := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				got = exit.ExitCode()
			}
			if tc.valid && len(output) != 0 {
				t.Errorf("unexpected shell output: %s", output)
			}
			if got != want {
				t.Errorf("shell exit=%d, want %d: %s", got, want, output)
			}
			if !tc.valid {
				if strings.Count(string(output), "\n") != 1 {
					t.Errorf("want one-line refusal, got %q", output)
				}
				t.Chdir(t.TempDir())
				var stderr bytes.Buffer
				if got := releaseCode([]string{tc.version, "revision"}, &stderr); got != 2 {
					t.Errorf("Go exit=%d, want 2", got)
				}
				if strings.Count(stderr.String(), "\n") != 1 {
					t.Errorf("want one-line Go refusal, got %q", stderr.String())
				}
				if _, err := os.Stat("dist"); !os.IsNotExist(err) {
					t.Errorf("invalid version created dist: %v", err)
				}
			}
		})
	}
}

func mustWorkingDirectory(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

type workflowStep struct {
	Name, Uses, Run string
	Env             map[string]string
	With            map[string]string
}

// Execute the workflow's actual run steps: validate alone, then release for each
// accepted version up to its first Go invocation. This tests the shell boundary
// too: ref_name is data, never code.
func TestWorkflowVerifyOrdering(t *testing.T) {
	if _, err := os.Stat(".github/workflows/release.yml"); os.IsNotExist(err) {
		t.Chdir("..")
	}
	data, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Needs []string
			Steps []workflowStep
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	validate := workflow.Jobs["validate"].Steps
	if len(validate) != 2 || !strings.HasPrefix(validate[0].Uses, "actions/checkout@") {
		t.Fatal("validate must be checkout followed by validation")
	}
	if validate[1].Env["RELEASE_VERSION"] != "${{ github.event_name == 'workflow_dispatch' && inputs.version || github.ref_name }}" {
		t.Fatal("validation must receive ref_name through environment immediately after checkout")
	}
	if !slices.Contains(workflow.Jobs["portable"].Needs, "validate") {
		t.Fatal("release must need validate")
	}
	release := workflow.Jobs["portable"].Steps
	bin := t.TempDir()
	marker := filepath.Join(bin, "build-called")
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\ntouch \"$BUILD_MARKER\"\nexit 97\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// run executes a job's run steps in order, stopping at the first failure.
	run := func(t *testing.T, steps []workflowStep, version string) (int, []byte) {
		for _, step := range steps {
			if step.Run == "" {
				continue
			}
			cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", step.Run)
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "BUILD_MARKER="+marker, "GITHUB_OUTPUT="+filepath.Join(bin, "outputs"), "GITHUB_WORKSPACE="+mustWorkingDirectory(t))
			for key, value := range step.Env {
				if strings.Contains(value, "github.ref_name") || value == "${{ needs.validate.outputs.version }}" {
					value = version
				}
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			output, err := cmd.CombinedOutput()
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				return exit.ExitCode(), output
			}
		}
		return 0, nil
	}
	versions := []string{"v0.1.0", "v1.0.0-rc.1", "v0.0.0", "v1.2.3-rc.0", "v123.456.789-rc.123", "v01.0.0", "v1.01.0", "v1.0.01", "v1.0.0-rc.01", "1.0.0", "v1.0", "v1.0.0.0", "v1.0.0-beta", "v1.0.0-rc", "v1.0.0-rc.1.2", "v1.0.0+meta", "v1.0.0-rc.1+meta", "", " ", "\tv1.0.0", "v1.0.0\n", "v1.0.0 ", "v1.0.0;false", "v1.0.0$(false)", "v1.0.0`false`", "v1.0.0|false"}
	for i, version := range versions {
		t.Run(fmt.Sprintf("%q", version), func(t *testing.T) {
			_ = os.Remove(marker)
			valid := i < 5
			// validate alone must accept or refuse the version without running Go.
			code, output := run(t, validate, version)
			_, markerErr := os.Stat(marker)
			want := 2
			if valid {
				want = 0
			}
			if code != want || markerErr == nil {
				t.Fatalf("validate exit=%d build=%t want exit=%d build=false: %s", code, markerErr == nil, want, output)
			}
			if !valid {
				if strings.Count(string(output), "\n") != 1 {
					t.Fatalf("want one refusal line: %q", output)
				}
				return
			}
			// release runs only after validate succeeds and must reach the Go build.
			code, output = run(t, release, version)
			if _, err := os.Stat(marker); code != 97 || err != nil {
				t.Fatalf("release exit=%d build=%t want exit=97 build=true: %s", code, err == nil, output)
			}
		})
	}
}

func TestWorkflowYAML(t *testing.T) {
	if _, err := os.Stat(".github/workflows"); os.IsNotExist(err) {
		t.Chdir("..")
	}
	paths, err := filepath.Glob(".github/workflows/*.yml")
	if err != nil || len(paths) == 0 {
		t.Fatalf("workflow paths: %v, %v", paths, err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var workflow yaml.Node
		if err := yaml.Unmarshal(data, &workflow); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestDormancyAlarm(t *testing.T) {
	if _, err := os.Stat(".github/workflows/vulncheck.yml"); os.IsNotExist(err) {
		t.Chdir("..")
	}
	data, err := os.ReadFile(".github/workflows/vulncheck.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct{ Steps []workflowStep }
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	steps := workflow.Jobs["maintenance"].Steps
	if len(steps) == 0 || steps[0].With["ref"] != "${{ github.event.repository.default_branch }}" {
		t.Fatal("maintenance must check out the default branch even for manual runs")
	}
	var script string
	for _, step := range steps {
		if step.Name == "Check repository activity" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("missing dormancy alarm")
	}
	bin := t.TempDir()
	for name, body := range map[string]string{
		"git":  "#!/bin/sh\nprintf '%s\\n' \"$DORMANCY_COMMIT_TIMESTAMP\"\n",
		"date": "#!/bin/sh\necho 2000000000\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	const cutoff = 45 * 24 * 60 * 60
	for _, age := range []int{0, cutoff - 1, cutoff, cutoff + 7*24*60*60} {
		cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), fmt.Sprintf("DORMANCY_COMMIT_TIMESTAMP=%d", 2000000000-age))
		output, err := cmd.CombinedOutput()
		if (err != nil) != (age >= cutoff) {
			t.Fatalf("age=%d: error=%v, output=%s", age, err, output)
		}
		if age >= cutoff && (!bytes.Contains(output, []byte("::error::")) || !bytes.Contains(output, []byte("maintenance commit")) || !bytes.Contains(output, []byte("re-enable Vulncheck"))) {
			t.Fatalf("alarm must explain recovery: %s", output)
		}
	}
}
