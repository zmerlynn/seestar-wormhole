//go:build release

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"cmp"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	if got := assetName("v0.5.0", "windows", "arm64", copyProgram); got != "seestar-copy-cli_v0.5.0_windows_arm64.exe" {
		t.Fatal(got)
	}
}

// Each program builds from its own command with the release settings; only
// the app gets the GUI tags and the Windows GUI subsystem.
func TestBuildBinaryCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in go is a shell script")
	}
	bin := t.TempDir()
	record := filepath.Join(bin, "args")
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RECORD\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RECORD", record)
	flags := "-s -w -X main.version=v0.5.0 -X main.revision=revision"
	for program, want := range map[string][]string{
		appProgram:  {"build", "-mod=vendor", "-trimpath", "-buildvcs=false", "-tags=gui,production", "-ldflags", flags + " -H windowsgui", "-o", "out", "./cmd/seestar-wormhole"},
		cliProgram:  {"build", "-mod=vendor", "-trimpath", "-buildvcs=false", "-ldflags", flags, "-o", "out", "./cmd/seestar-wormhole"},
		copyProgram: {"build", "-mod=vendor", "-trimpath", "-buildvcs=false", "-ldflags", flags, "-o", "out", "./cmd/seestar-copy-cli"},
	} {
		if err := buildBinary("v0.5.0", "revision", "windows", "amd64", program, "out"); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(record)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"); !slices.Equal(got, want) {
			t.Errorf("%s: go %q, want %q", program, got, want)
		}
	}
}

// The DMG carries both CLIs, each built universal, signed like the app and
// placed in the window. The Mac scripts run only on macOS, so this reads them.
func TestMacPackageCarriesBothCLIs(t *testing.T) {
	if _, err := os.Stat("scripts/release.sh"); os.IsNotExist(err) {
		t.Chdir("..")
	}
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	hasLine := func(text, want string) bool {
		return slices.ContainsFunc(strings.Split(text, "\n"), func(line string) bool { return strings.TrimSpace(line) == want })
	}
	build, pack, settings := read("scripts/build-gui-mac.sh"), read("scripts/package-gui-mac.sh"), read("scripts/dmg-settings.py")
	for _, want := range []string{
		"for task_cli in seestar-wormhole-cli:seestar-wormhole seestar-copy-cli:seestar-copy-cli; do",
		"for task_cli in seestar-wormhole-cli seestar-copy-cli; do",
		`lipo "$1/universal/$task_cli" -verify_arch arm64 x86_64`,
	} {
		if !hasLine(build, want) {
			t.Errorf("build-gui-mac.sh lacks %q", want)
		}
	}
	for _, want := range []string{
		`task_copy_cli="$(dirname "$task_app")/seestar-copy-cli"`,
		`for task_program in "$task_app" "$task_cli" "$task_copy_cli"; do`,
		`codesign --force --sign - "$task_copy_cli"`,
		`codesign --verify --strict --verbose=2 "$task_copy_cli"`,
		`cp "$task_cli" "$task_copy_cli" "$task_temp/image/"`,
	} {
		if !hasLine(pack, want) {
			t.Errorf("package-gui-mac.sh lacks %q", want)
		}
	}
	for _, program := range cliPrograms {
		if strings.Count(settings, `"`+program+`"`) != 2 {
			t.Errorf("dmg-settings.py must list and place %s", program)
		}
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

func fakeBuild(version, revision, goos, arch, program, path string) error {
	return os.WriteFile(path, []byte(fmt.Sprintf("%s %s %s %s %s", version, revision, goos, arch, program)), 0600)
}

func nativeParts(t *testing.T) {
	t.Helper()
	for _, arch := range []string{"amd64", "arm64"} {
		if err := fakeBuild("v0.5.0", "revision", "linux", arch, appProgram, filepath.Join("dist/parts", assetName("v0.5.0", "linux", arch, appProgram))); err != nil {
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
	var builds []string
	if err := release("v0.5.0", "revision", archiveTime, func(v, r, goos, arch, program, path string) error {
		builds = append(builds, program+" "+goos+"/"+arch)
		if program == appProgram && goos != "windows" {
			t.Fatal("container attempted native UI build")
		}
		return fakeBuild(v, r, goos, arch, program, path)
	}); err != nil {
		t.Fatal(err)
	}
	// Both CLIs on every platform, and the cgo-free Windows app.
	var wantBuilds []string
	for _, platform := range platforms {
		wantBuilds = append(wantBuilds, "seestar-wormhole-cli "+platform, "seestar-copy-cli "+platform)
		if strings.HasPrefix(platform, "windows/") {
			wantBuilds = append(wantBuilds, "seestar-wormhole "+platform)
		}
	}
	slices.Sort(builds)
	slices.Sort(wantBuilds)
	if !slices.Equal(builds, wantBuilds) {
		t.Fatalf("builds=%v, want %v", builds, wantBuilds)
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
			programs := []string{appProgram, cliProgram, copyProgram}
			for _, program := range programs {
				if err := fakeBuild("v0.5.0", "revision", goos, arch, program, filepath.Join("parts", assetName("v0.5.0", goos, arch, program))); err != nil {
					t.Fatal(err)
				}
			}
			name := archiveName("v0.5.0", goos, arch)
			var first []byte
			for run := range 2 {
				// Change source metadata: the archive must ignore it.
				inputs := []string{"LICENSE", "NOTICE"}
				for _, program := range programs {
					inputs = append(inputs, filepath.Join("parts", assetName("v0.5.0", goos, arch, program)))
				}
				for _, input := range inputs {
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
			wantNames := []string{"LICENSE", "NOTICE", "seestar-copy-cli", "seestar-wormhole", "seestar-wormhole-cli"}
			if goos == "windows" {
				wantNames = []string{"LICENSE", "NOTICE", "seestar-copy-cli.exe", "seestar-wormhole-cli.exe", "seestar-wormhole.exe"}
			}
			var names []string
			check := func(name string, mode os.FileMode, stamp time.Time, data []byte) {
				t.Helper()
				names = append(names, name)
				wantMode, wantData := os.FileMode(0644), name+" fixture"
				if strings.HasPrefix(name, "seestar-") {
					wantMode = 0755
					wantData = fmt.Sprintf("v0.5.0 revision %s %s %s", goos, arch, strings.TrimSuffix(name, ext))
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
	err := release("v0.5.0", "revision", archiveTime, func(_, _, _, _, _, path string) error {
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
	for _, part := range []string{"seestar-wormhole-cli_v0.5.0_linux_amd64", "seestar-copy-cli_v0.5.0_linux_amd64"} {
		if _, err := os.Stat(filepath.Join("dist/parts", part)); err != nil {
			t.Fatal("failed assembly lost parts")
		}
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
	downloaded, dmgDependencies := false, false
	for _, step := range workflow.Jobs["macos-ui"].Steps {
		if strings.Contains(step.Run, `python3 -m venv "$RUNNER_TEMP/dmg-venv"`) &&
			strings.Contains(step.Run, `source "$RUNNER_TEMP/dmg-venv/bin/activate"`) &&
			strings.Contains(step.Run, "python3 -m pip install --require-hashes --only-binary :all: -r scripts/dmg-requirements.txt") &&
			strings.Contains(step.Run, `echo "$RUNNER_TEMP/dmg-venv/bin" >> "$GITHUB_PATH"`) {
			dmgDependencies = true
		}
		if strings.Contains(step.Run, "./scripts/build-gui-mac.sh") && !dmgDependencies {
			t.Fatal("install pinned DMG dependencies in a venv before the Mac build")
		}
		if strings.HasPrefix(step.Uses, "actions/download-artifact@") && step.With["name"] == "release-part-portable" && step.With["path"] == "${{ runner.temp }}/mac-ui/cli" {
			downloaded = true
		}
	}
	if !downloaded {
		t.Fatal("missing Mac CLI parts dependency")
	}
	if !dmgDependencies {
		t.Fatal("missing pinned DMG dependencies")
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

// Each release builder checks the executables it built, before uploading them,
// against the modules Go recorded in them.
func TestWorkflowChecksShippedLicenses(t *testing.T) {
	if _, err := os.Stat(".github/workflows/release.yml"); os.IsNotExist(err) {
		t.Chdir("..")
	}
	data, err := os.ReadFile(".github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct{ Steps []workflowStep }
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ job, build, check string }{
		{"portable", `./scripts/release.sh "$RELEASE_VERSION"`,
			`go run ./scripts/checklicenses "dist/parts/seestar-wormhole-cli_${RELEASE_VERSION}_linux_amd64" "dist/parts/seestar-copy-cli_${RELEASE_VERSION}_linux_amd64"`},
		{"linux-ui", "./cmd/seestar-wormhole",
			`go run ./scripts/checklicenses "dist/seestar-wormhole_${RELEASE_VERSION}_linux_${GOARCH}"`},
		{"macos-ui", "./scripts/build-gui-mac.sh",
			`go run ./scripts/checklicenses "$RUNNER_TEMP/mac-ui/arm64/seestar-wormhole.app/Contents/MacOS/seestar-wormhole" \` + "\n" +
				`  "$RUNNER_TEMP/mac-ui/cli/seestar-wormhole-cli_${RELEASE_VERSION}_darwin_arm64" \` + "\n" +
				`  "$RUNNER_TEMP/mac-ui/cli/seestar-copy-cli_${RELEASE_VERSION}_darwin_arm64"`},
	} {
		built, checked := false, false
		for _, step := range workflow.Jobs[tc.job].Steps {
			if strings.Contains(step.Run, tc.build) {
				built = true
			}
			if strings.Contains(step.Run, tc.check) {
				if !built || step.Env["RELEASE_VERSION"] != "${{ needs.validate.outputs.version }}" {
					t.Errorf("%s checks licenses before its build or without the release version", tc.job)
				}
				checked = true
			}
			if strings.HasPrefix(step.Uses, "actions/upload-artifact@") && !checked {
				t.Errorf("%s uploads before checking licenses", tc.job)
			}
		}
		if !checked {
			t.Errorf("%s does not check its executables' licenses", tc.job)
		}
	}
}

// verifyLines are the commands CI's Linux job must run: the gofmt check, the
// race suite over the whole module, the license notices, and the
// repository's own checks.
var verifyLines = []string{
	`unformatted=$(find . \( -type d -name '.*' ! -name . -o -path ./vendor \) -prune -o -type f -name '*.go' -print | xargs gofmt -l)`,
	`if [ -n "$unformatted" ]; then`,
	"exit 1",
	"go run ./scripts/generate-licenses.go --check",
	"go run ./scripts/checklicenses",
	"go test -race -count=2 -timeout 20m ./...",
	"./scripts/test-check.sh",
	"CGO_ENABLED=0 GOOS=${target%/*} GOARCH=${target#*/} go vet -p 2 ./cmd/seestar-copy-cli ./internal/copy/...",
	"go vet -p 2 -tags smbintegration ./internal/copy/remote ./internal/copy/app",
}

// CI's Linux job must keep the checks the copy workflow ran before the merge.
func TestWorkflowVerifyChecks(t *testing.T) {
	if _, err := os.Stat(".github/workflows/ci.yml"); os.IsNotExist(err) {
		t.Chdir("..")
	}
	data, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkVerify(data); err != nil {
		t.Fatal(err)
	}
}

// The verify check refuses edits that drop a required check or let it be
// skipped or fail, and accepts harmless ones. Each edit works on the parsed
// workflow and finds the step it changes by its run block, so renaming or
// moving a step does not break the cases.
func TestWorkflowVerifyChecksRefuse(t *testing.T) {
	if _, err := os.Stat(".github/workflows/ci.yml"); os.IsNotExist(err) {
		t.Chdir("..")
	}
	data, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	const race = "go test -race -count=2 -timeout 20m ./..."
	get := func(m *yaml.Node, key string) *yaml.Node {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				return m.Content[i+1]
			}
		}
		return nil
	}
	set := func(m *yaml.Node, key, value string) {
		var v yaml.Node
		if err := yaml.Unmarshal([]byte(value), &v); err != nil {
			t.Fatal(err)
		}
		if old := get(m, key); old != nil {
			*old = *v.Content[0]
			return
		}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v.Content[0])
	}
	// rewrite replaces line, wherever a verify step's run block has it.
	rewrite := func(job *yaml.Node, line string, with ...string) {
		for _, step := range get(job, "steps").Content {
			if run := get(step, "run"); run != nil {
				lines := strings.Split(run.Value, "\n")
				for i := len(lines) - 1; i >= 0; i-- {
					if strings.TrimSpace(lines[i]) == line {
						lines = slices.Replace(lines, i, i+1, with...)
					}
				}
				run.Value = strings.Join(lines, "\n")
			}
		}
	}
	type edit struct {
		name string
		fn   func(root, job, step *yaml.Node)
		ok   bool
	}
	edits := []edit{
		{"race commented", func(_, job, _ *yaml.Node) { rewrite(job, race, "# "+race) }, false},
		{"step if false", func(_, _, step *yaml.Node) { set(step, "if", "${{ false }}") }, false},
		{"step continue-on-error", func(_, _, step *yaml.Node) { set(step, "continue-on-error", "true") }, false},
		{"step shell", func(_, _, step *yaml.Node) { set(step, "shell", "pwsh") }, false},
		{"job if", func(_, job, _ *yaml.Node) { set(job, "if", "github.event_name == 'push'") }, false},
		{"job continue-on-error", func(_, job, _ *yaml.Node) { set(job, "continue-on-error", "true") }, false},
		{"job shell", func(_, job, _ *yaml.Node) { set(job, "defaults", "run: {shell: python}") }, false},
		{"workflow shell", func(root, _, _ *yaml.Node) { set(root, "defaults", "run: {shell: python}") }, false},
		{"step renamed", func(_, _, step *yaml.Node) { set(step, "name", "Verify") }, true},
		{"step if true", func(_, _, step *yaml.Node) { set(step, "if", "'${{ true }}'") }, true},
		{"step continue-on-error false", func(_, _, step *yaml.Node) { set(step, "continue-on-error", "false") }, true},
		{"step shell bash", func(_, _, step *yaml.Node) { set(step, "shell", "bash") }, true},
		{"job if true", func(_, job, _ *yaml.Node) { set(job, "if", "'${{ true }}'") }, true},
		{"job continue-on-error false", func(_, job, _ *yaml.Node) { set(job, "continue-on-error", "false") }, true},
		{"job shell bash", func(_, job, _ *yaml.Node) { set(job, "defaults", "run: {shell: bash}") }, true},
		{"job shell sh", func(_, job, _ *yaml.Node) { set(job, "defaults", "run: {shell: sh}") }, true},
		{"workflow shell bash", func(root, _, _ *yaml.Node) { set(root, "defaults", "run: {shell: bash}") }, true},
		{"workflow shell overridden", func(root, job, _ *yaml.Node) {
			set(root, "defaults", "run: {shell: python}")
			set(job, "defaults", "run: {shell: sh}")
		}, true},
	}
	for _, line := range verifyLines {
		edits = append(edits, edit{"without " + line, func(_, job, _ *yaml.Node) { rewrite(job, line) }, false})
	}
	for _, tt := range edits {
		t.Run(tt.name, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			root := doc.Content[0]
			job := get(get(root, "jobs"), "verify")
			var step *yaml.Node
			for _, s := range get(job, "steps").Content {
				if run := get(s, "run"); run != nil && slices.Contains(strings.Split(run.Value, "\n"), race) {
					step = s
				}
			}
			if step == nil {
				t.Fatalf("ci.yml's verify job has no step running %s", race)
			}
			tt.fn(root, job, step)
			edited, err := yaml.Marshal(&doc)
			if err != nil {
				t.Fatal(err)
			}
			err = checkVerify(edited)
			if (err == nil) != tt.ok {
				t.Errorf("accepted=%t, want %t: %v", err == nil, tt.ok, err)
			}
		})
	}
}

// checkVerify checks ci.yml's verify job structurally: each required line is
// a whole line of a step's run block, and nothing at the workflow, job or
// step level can skip that step, let it fail or give it a shell other than
// bash or sh. It guards against accidental removal; review covers deliberate
// obfuscation.
func checkVerify(data []byte) error {
	type shellDefaults struct{ Run struct{ Shell string } }
	var workflow struct {
		Defaults shellDefaults
		Jobs     map[string]struct {
			If              string
			ContinueOnError string `yaml:"continue-on-error"`
			Defaults        shellDefaults
			Steps           []struct {
				Name, Run, If, Shell string
				ContinueOnError      string `yaml:"continue-on-error"`
			}
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		return err
	}
	verify, ok := workflow.Jobs["verify"]
	if !ok {
		return errors.New("ci.yml has no verify job")
	}
	var problems []error
	ifs, continues, shells := []string{"", "true", "${{ true }}"}, []string{"", "false"}, []string{"", "bash", "sh"}
	if !slices.Contains(ifs, verify.If) || !slices.Contains(continues, verify.ContinueOnError) {
		problems = append(problems, fmt.Errorf("verify has if %q, continue-on-error %q", verify.If, verify.ContinueOnError))
	}
	found := map[string]bool{}
	for _, step := range verify.Steps {
		var carries bool
		for line := range strings.SplitSeq(step.Run, "\n") {
			line = strings.TrimSpace(line)
			if slices.Contains(verifyLines, line) {
				found[line], carries = true, true
			}
		}
		shell := cmp.Or(step.Shell, verify.Defaults.Run.Shell, workflow.Defaults.Run.Shell)
		if carries && (!slices.Contains(ifs, step.If) || !slices.Contains(continues, step.ContinueOnError) || !slices.Contains(shells, shell)) {
			problems = append(problems, fmt.Errorf("verify step %q has if %q, continue-on-error %q, shell %q",
				step.Name, step.If, step.ContinueOnError, shell))
		}
	}
	for _, line := range verifyLines {
		if !found[line] {
			problems = append(problems, fmt.Errorf("verify must run %s", line))
		}
	}
	return errors.Join(problems...)
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

// pageTestLines are the commands macOS's native job must run so the page
// regressions execute in JavaScriptCore, and fail rather than skip.
var pageTestLines = []string{
	"jsc=/System/Library/Frameworks/JavaScriptCore.framework/Versions/Current/Helpers/jsc",
	`if [[ ! -x "$jsc" ]]; then`,
	`PATH="$RUNNER_TEMP/jsc:$PATH" go test -count=1 -v ./internal/ui | tee "$RUNNER_TEMP/page-tests.log"`,
	`if grep -q 'JavaScriptCore shell is not installed' "$RUNNER_TEMP/page-tests.log"; then`,
	`grep -q 'PASS: TestCopySetupPage' "$RUNNER_TEMP/page-tests.log"`,
}

// CI runs the page regressions on macOS, where jsc is, and a missing jsc
// or a skipped page test fails the job.
func TestWorkflowPageTests(t *testing.T) {
	if _, err := os.Stat(".github/workflows/ci.yml"); os.IsNotExist(err) {
		t.Chdir("..")
	}
	data, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			If              string
			ContinueOnError string `yaml:"continue-on-error"`
			Strategy        struct{ Matrix struct{ OS []string } }
			Defaults        struct{ Run struct{ Shell string } }
			Steps           []struct {
				Name, Run, If, Shell string
				ContinueOnError      string `yaml:"continue-on-error"`
			}
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	native := workflow.Jobs["native"]
	if native.If != "" || native.ContinueOnError != "" || native.Defaults.Run.Shell != "bash" ||
		!slices.ContainsFunc(native.Strategy.Matrix.OS, func(os string) bool { return strings.HasPrefix(os, "macos-") }) {
		t.Fatalf("the native job must run on macOS, in bash, unconditionally: %+v", native.Strategy)
	}
	for _, step := range native.Steps {
		missing := slices.DeleteFunc(slices.Clone(pageTestLines), func(want string) bool {
			for line := range strings.SplitSeq(step.Run, "\n") {
				if strings.TrimSpace(line) == want {
					return true
				}
			}
			return false
		})
		if len(missing) == len(pageTestLines) {
			continue
		}
		if len(missing) != 0 {
			t.Fatalf("step %q lacks %q", step.Name, missing)
		}
		if step.If != "runner.os == 'macOS'" || step.ContinueOnError != "" || step.Shell != "" {
			t.Fatalf("step %q has if %q, continue-on-error %q, shell %q", step.Name, step.If, step.ContinueOnError, step.Shell)
		}
		return
	}
	t.Fatal("no native step runs the page tests in JavaScriptCore")
}

// browserTestLines are the commands copy-browser must run so the app's
// Playwright tests have the page's server and the CLI.
var browserTestLines = []string{
	`echo "CLI_BIN=$RUNNER_TEMP/seestar-copy-cli" >> "$GITHUB_ENV"`,
	`go test -c -o "$RUNNER_TEMP/ui.test" ./internal/ui`,
	`echo "UI_TEST_BIN=$RUNNER_TEMP/ui.test" >> "$GITHUB_ENV"`,
}

// CI runs every Playwright spec, the app's with them, in Chromium and
// WebKit, against the page internal/ui's TestBrowserServer serves.
func TestWorkflowBrowserTests(t *testing.T) {
	if _, err := os.Stat(".github/workflows/ci.yml"); os.IsNotExist(err) {
		t.Chdir("..")
	}
	data, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			If              string
			ContinueOnError string `yaml:"continue-on-error"`
			Steps           []struct {
				Name, Run, If    string
				WorkingDirectory string `yaml:"working-directory"`
				ContinueOnError  string `yaml:"continue-on-error"`
			}
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	job, ok := workflow.Jobs["copy-browser"]
	if !ok || job.If != "" || job.ContinueOnError != "" {
		t.Fatalf("the copy-browser job must run unconditionally: if %q, continue-on-error %q", job.If, job.ContinueOnError)
	}
	var built, installed, ran bool
	for _, step := range job.Steps {
		if step.If != "" || step.ContinueOnError != "" {
			continue
		}
		lines := map[string]bool{}
		for line := range strings.SplitSeq(step.Run, "\n") {
			lines[strings.TrimSpace(line)] = true
		}
		built = built || !slices.ContainsFunc(browserTestLines, func(want string) bool { return !lines[want] })
		if step.WorkingDirectory == "test/browser" {
			installed = installed || lines["npx playwright install --with-deps chromium webkit"]
			ran = ran || (installed && lines["npx playwright test"])
		}
	}
	if !built || !installed || !ran {
		t.Fatalf("copy-browser must build the page's server and the CLI (%t), install Chromium and WebKit (%t), then run Playwright (%t)", built, installed, ran)
	}

	config, err := os.ReadFile("test/browser/playwright.config.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"process.env.UI_TEST_BIN", "process.env.CLI_BIN", "name: 'chromium'", "name: 'webkit'"} {
		if !bytes.Contains(config, []byte(want)) {
			t.Fatalf("playwright.config.js lacks %q", want)
		}
	}
	// A testMatch would leave a spec out.
	if bytes.Contains(config, []byte("testMatch")) || bytes.Contains(config, []byte("testIgnore")) {
		t.Fatal("playwright.config.js must run every spec")
	}
	for _, spec := range []string{"app.spec.js", "copy.spec.js", "free.spec.js", "browse.spec.js"} {
		if _, err := os.Stat(filepath.Join("test/browser/tests", spec)); err != nil {
			t.Fatal(err)
		}
	}
	launcher, err := os.ReadFile("test/browser/tests/app.js")
	if err != nil {
		t.Fatal(err)
	}
	server, err := os.ReadFile("internal/ui/browser_server_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(launcher, []byte("'-test.run=^TestBrowserServer$'")) || !bytes.Contains(server, []byte("func TestBrowserServer(t *testing.T)")) {
		t.Fatal("test/browser/tests/app.js must start internal/ui's TestBrowserServer")
	}
}
