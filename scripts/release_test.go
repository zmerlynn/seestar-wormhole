//go:build release

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

var archiveTime = time.Unix(1793441237, 0).UTC()

func TestReleaseAssetNames(t *testing.T) {
	want := []string{
		"seestar-wormhole_v0.5.0_darwin_universal.dmg",
		"seestar-wormhole_v0.5.0_linux_amd64.tar.gz", "seestar-wormhole_v0.5.0_linux_arm64.tar.gz",
		"seestar-wormhole_v0.5.0_windows_amd64.zip", "seestar-wormhole_v0.5.0_windows_arm64.zip",
	}
	if got := expectedAssets("v0.5.0"); !slices.Equal(got, want) {
		t.Fatalf("assets=%v, want %v", got, want)
	}
	if got := archiveName("v0.5.0-rc.1", "windows", "arm64"); got != "seestar-wormhole_v0.5.0-rc.1_windows_arm64.zip" {
		t.Fatal(got)
	}
}

func archiveFixtures(t *testing.T) {
	t.Helper()
	for _, name := range []string{"LICENSE", "NOTICE"} {
		if err := os.WriteFile(name, []byte(name+" fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func fakeBuild(version, revision, goos, arch string, gui bool, path string) error {
	return os.WriteFile(path, []byte(fmt.Sprintf("%s %s %s %s gui=%t", version, revision, goos, arch, gui)), 0600)
}

func nativeParts(t *testing.T) {
	t.Helper()
	for _, arch := range []string{"amd64", "arm64"} {
		if err := fakeBuild("v0.5.0", "revision", "linux", arch, true, filepath.Join("dist/parts", assetName("v0.5.0", "linux", arch, true))); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile("dist/parts/seestar-wormhole_v0.5.0_darwin_universal.dmg", []byte("synthetic dmg"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseArchivesAndChecksums(t *testing.T) {
	t.Chdir(t.TempDir())
	archiveFixtures(t)
	builds := 0
	if err := release("v0.5.0", "revision", archiveTime, func(v, r, goos, arch string, gui bool, path string) error {
		builds++
		if gui && goos != "windows" {
			t.Fatal("container attempted native UI build")
		}
		return fakeBuild(v, r, goos, arch, gui, path)
	}); err != nil {
		t.Fatal(err)
	}
	if builds != 8 {
		t.Fatalf("builds=%d, want eight", builds)
	}
	if _, err := os.Stat("dist/SHA256SUMS"); !os.IsNotExist(err) {
		t.Fatal("portable build must not publish a partial checksum file")
	}
	if err := finalChecksums("v0.5.0", archiveTime); err == nil {
		t.Fatal("accepted incomplete release")
	}
	nativeParts(t)
	if err := os.WriteFile("dist/parts/unexpected", nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := finalChecksums("v0.5.0", archiveTime); err == nil {
		t.Fatal("accepted unexpected part")
	}
	if err := os.Remove("dist/parts/unexpected"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("dist/unexpected", nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := finalChecksums("v0.5.0", archiveTime); err == nil {
		t.Fatal("accepted unexpected asset")
	}
	if err := os.Remove("dist/unexpected"); err != nil {
		t.Fatal(err)
	}
	if err := finalChecksums("v0.5.0", archiveTime); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir("dist")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"SHA256SUMS", "seestar-wormhole_v0.5.0_darwin_universal.dmg", "seestar-wormhole_v0.5.0_linux_amd64.tar.gz", "seestar-wormhole_v0.5.0_linux_arm64.tar.gz", "seestar-wormhole_v0.5.0_windows_amd64.zip", "seestar-wormhole_v0.5.0_windows_arm64.zip"}
	var got []string
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	if !slices.Equal(got, want) {
		t.Fatalf("final inventory=%v", got)
	}
	var sums strings.Builder
	for _, name := range want[1:] {
		data, err := os.ReadFile(filepath.Join("dist", name))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(data), name)
	}
	data, err := os.ReadFile("dist/SHA256SUMS")
	if err != nil || string(data) != sums.String() {
		t.Fatalf("checksums=%s err=%v", data, err)
	}
}

func TestArchiveContentsModesAndDeterminism(t *testing.T) {
	t.Chdir(t.TempDir())
	archiveFixtures(t)
	if err := os.Mkdir("parts", 0755); err != nil {
		t.Fatal(err)
	}
	for _, goos := range []string{"linux", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			for _, gui := range []bool{false, true} {
				if err := fakeBuild("v0.5.0", "revision", goos, arch, gui, filepath.Join("parts", assetName("v0.5.0", goos, arch, gui))); err != nil {
					t.Fatal(err)
				}
			}
			name := archiveName("v0.5.0", goos, arch)
			var first []byte
			for run := range 2 {
				// Change source metadata: the archive must ignore it.
				for _, input := range []string{"LICENSE", "NOTICE", filepath.Join("parts", assetName("v0.5.0", goos, arch, true)), filepath.Join("parts", assetName("v0.5.0", goos, arch, false))} {
					if err := os.Chtimes(input, archiveTime.Add(time.Duration(run)*time.Hour), archiveTime.Add(time.Duration(run)*time.Hour)); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(input, os.FileMode(0600+run*4)); err != nil {
						t.Fatal(err)
					}
				}
				if err := writeArchive(".", "parts", "v0.5.0", goos, arch, archiveTime); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				if run == 0 {
					first = data
				} else if !bytes.Equal(first, data) {
					t.Fatalf("%s is not deterministic", name)
				}
			}
			ext := ""
			if goos == "windows" {
				ext = ".exe"
			}
			wantNames := []string{"LICENSE", "NOTICE", "seestar-wormhole" + ext, "seestar-wormhole-cli" + ext}
			if goos == "windows" {
				wantNames = []string{"LICENSE", "NOTICE", "seestar-wormhole-cli.exe", "seestar-wormhole.exe"}
			}
			var names []string
			check := func(name string, mode os.FileMode, stamp time.Time, data []byte) {
				t.Helper()
				names = append(names, name)
				wantMode, wantData := os.FileMode(0644), name+" fixture"
				if strings.HasPrefix(name, "seestar-wormhole") {
					wantMode = 0755
					wantData = fmt.Sprintf("v0.5.0 revision %s %s gui=%t", goos, arch, !strings.Contains(name, "-cli"))
				}
				if mode != wantMode || !stamp.Equal(archiveTime) || string(data) != wantData {
					t.Fatalf("%s mode=%v time=%v data=%q", name, mode, stamp, data)
				}
			}
			if goos == "windows" {
				zr, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range zr.File {
					if f.Method != zip.Deflate {
						t.Fatal("zip must use deflate")
					}
					r, err := f.Open()
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(r)
					if err != nil {
						t.Fatal(err)
					}
					if err := r.Close(); err != nil {
						t.Fatal(err)
					}
					check(f.Name, f.Mode(), f.Modified, data)
				}
			} else {
				gz, err := gzip.NewReader(bytes.NewReader(first))
				if err != nil {
					t.Fatal(err)
				}
				if gz.Name != "" || !gz.ModTime.IsZero() || len(gz.Extra) != 0 || gz.Comment != "" {
					t.Fatal("nonempty gzip metadata")
				}
				tr := tar.NewReader(gz)
				for {
					h, err := tr.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || h.Typeflag != tar.TypeReg || !h.AccessTime.IsZero() || !h.ChangeTime.IsZero() {
						t.Fatalf("tar metadata: %+v", h)
					}
					data, err := io.ReadAll(tr)
					if err != nil {
						t.Fatal(err)
					}
					check(h.Name, os.FileMode(h.Mode), h.ModTime, data)
				}
				if err := gz.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(names, wantNames) {
				t.Fatalf("members=%v, want %v", names, wantNames)
			}
		}
	}
}

func TestChecksumsHashEachFileAndSortByName(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{"a": "abc", "z": ""} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeChecksums(dir, []string{"z", "a"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad  a\n" +
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  z\n"
	if err != nil || string(data) != want {
		t.Fatalf("SHA256SUMS=%q err=%v", data, err)
	}
}

func TestReleaseDoesNotPublishFailedBuilds(t *testing.T) {
	t.Chdir(t.TempDir())
	err := release("v0.5.0", "revision", archiveTime, func(_, _, _, _ string, _ bool, path string) error {
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

func TestFinalAssemblyFailureKeepsParts(t *testing.T) {
	t.Chdir(t.TempDir())
	archiveFixtures(t)
	if err := release("v0.5.0", "revision", archiveTime, fakeBuild); err != nil {
		t.Fatal(err)
	}
	nativeParts(t)
	name := "dist/seestar-wormhole_v0.5.0_windows_amd64.zip"
	before, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove("NOTICE"); err != nil {
		t.Fatal(err)
	}
	if err := finalChecksums("v0.5.0", archiveTime); err == nil {
		t.Fatal("ignored archive failure")
	}
	after, err := os.ReadFile(name)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed assembly replaced published output")
	}
	if _, err := os.Stat("dist/SHA256SUMS"); !os.IsNotExist(err) {
		t.Fatal("failed assembly published checksums")
	}
	if _, err := os.Stat("dist/parts/seestar-wormhole-cli_v0.5.0_linux_amd64"); err != nil {
		t.Fatal("failed assembly lost parts")
	}
	stages, err := filepath.Glob("dist/.release-*")
	if err != nil || len(stages) != 0 {
		t.Fatalf("staging leak: %v %v", stages, err)
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
	Name, Uses, Run, If string
	Env                 map[string]string
	With                map[string]string
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

func TestWorkflowDispatchOnlyUploadsArtifacts(t *testing.T) {
	if _, err := os.Stat(".github/workflows/release.yml"); os.IsNotExist(err) {
		t.Chdir("..")
	}
	data, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On   map[string]any
		Jobs map[string]struct{ Steps []workflowStep }
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	if _, ok := workflow.On["workflow_dispatch"]; !ok {
		t.Fatal("missing artifact-only dispatch")
	}
	upload, publish := false, false
	for _, step := range workflow.Jobs["release"].Steps {
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			upload = true
		}
		if strings.Contains(step.With["script"], "createRelease") {
			publish = true
			if step.If != "github.event_name == 'push'" {
				t.Fatal("manual dispatch can create a release")
			}
		}
	}
	if !upload || !publish {
		t.Fatal("missing final artifact upload or guarded release creation")
	}
}

func TestWorkflowArchiveAssembly(t *testing.T) {
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
	if !slices.Contains(workflow.Jobs["macos-ui"].Needs, "portable") {
		t.Fatal("Mac must reuse portable Darwin CLIs")
	}
	downloaded := false
	for _, step := range workflow.Jobs["macos-ui"].Steps {
		if strings.HasPrefix(step.Uses, "actions/download-artifact@") && step.With["name"] == "release-part-portable" && step.With["path"] == "${{ runner.temp }}/mac-ui/cli" {
			downloaded = true
		}
	}
	if !downloaded {
		t.Fatal("missing Mac CLI parts dependency")
	}
	for _, step := range workflow.Jobs["portable"].Steps {
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") && step.With["path"] != "dist/parts/*" {
			t.Fatal("portable must transfer raw parts")
		}
	}
	assembled, attested := false, false
	for _, step := range workflow.Jobs["release"].Steps {
		if strings.HasPrefix(step.Uses, "actions/download-artifact@") && (step.With["path"] != "dist/parts" || step.With["pattern"] != "release-part-*") {
			t.Fatal("final job must gather only builder parts")
		}
		if strings.Contains(step.Run, "./scripts/release.sh --checksums") && strings.Contains(step.Run, "sha256sum --check SHA256SUMS") {
			assembled = true
		}
		if strings.HasPrefix(step.Uses, "actions/attest-build-provenance@") {
			if !assembled || step.With["subject-checksums"] != "dist/SHA256SUMS" {
				t.Fatal("attest only assembled downloads using the single checksum file")
			}
			attested = true
		}
		if strings.Contains(step.With["script"], "createRelease") && !strings.Contains(step.With["script"], "files.length !== 6") {
			t.Fatal("release must contain five downloads plus SHA256SUMS")
		}
	}
	if !assembled || !attested {
		t.Fatal("missing final assembly or attestation")
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
