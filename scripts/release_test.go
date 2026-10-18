//go:build release

package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseArchiveContents(t *testing.T) {
	// Named-file tests run from the caller's directory; also support scripts/.
	if _, err := os.Stat("README.md"); os.IsNotExist(err) {
		t.Chdir("..")
	}
	for _, windows := range []bool{false, true} {
		t.Run(fmt.Sprintf("windows=%t", windows), func(t *testing.T) {
			binary := "seestar-wormhole"
			if windows {
				binary += ".exe"
			}
			want := checkoutReleaseFiles(t, binary)
			dir := t.TempDir()
			binaryPath := filepath.Join(dir, binary)
			if err := os.WriteFile(binaryPath, []byte("test executable"), 0600); err != nil {
				t.Fatal(err)
			}
			archivePath := filepath.Join(dir, "archive")
			if err := archive(archivePath, binaryPath, binary, windows); err != nil {
				t.Fatal(err)
			}
			var names []string
			contents := map[string][]byte{}
			add := func(name string, mode os.FileMode, r io.Reader) {
				t.Helper()
				wantMode := os.FileMode(0644)
				if name == binary {
					wantMode = 0755
				}
				if mode.Perm() != wantMode {
					t.Errorf("%s: mode %o, want %o", name, mode.Perm(), wantMode)
				}
				data, err := io.ReadAll(r)
				if err != nil {
					t.Fatal(err)
				}
				contents[name] = data
				names = append(names, name)
			}
			if windows {
				zr, err := zip.OpenReader(archivePath)
				if err != nil {
					t.Fatal(err)
				}
				defer zr.Close()
				for _, f := range zr.File {
					if !f.Modified.Equal(archiveTime) {
						t.Errorf("%s: timestamp %v, want %v", f.Name, f.Modified, archiveTime)
					}
					r, err := f.Open()
					if err != nil {
						t.Fatal(err)
					}
					add(f.Name, f.Mode(), r)
					if err := r.Close(); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				f, err := os.Open(archivePath)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				gz, err := gzip.NewReader(f)
				if err != nil {
					t.Fatal(err)
				}
				defer gz.Close()
				tr := tar.NewReader(gz)
				for {
					h, err := tr.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if !h.ModTime.Equal(archiveTime) || h.Uid != 0 || h.Gid != 0 {
						t.Errorf("%s: noncanonical timestamp/ownership: %+v", h.Name, h)
					}
					add(h.Name, h.FileInfo().Mode(), tr)
				}
			}
			if !slices.Equal(names, want) {
				t.Errorf("archive entries = %v, want exact checkout file set %v", names, want)
			}
			checkPackagedDevelopmentLinks(t, contents)
			t.Logf("checked exact release archive file set: %v", names)
		})
	}
}

func checkPackagedDevelopmentLinks(t *testing.T, contents map[string][]byte) {
	t.Helper()
	// No Markdown parser is vendored. Check only the literal development link
	// that the release writes, rather than implementing a Markdown parser.
	const span = "[How it works and how to hack on it](https://github.com/zmerlynn/seestar-wormhole/blob/main/docs/development/README.md)"
	if got := bytes.Count(contents["README.md"], []byte(span)); got != 1 {
		t.Errorf("README.md: source-repository development links=%d want 1", got)
	}
	if _, ok := contents["docs/development/README.md"]; ok {
		t.Fatal("development guide unexpectedly included in release")
	}
}

func checkoutReleaseFiles(t *testing.T, binary string) []string {
	t.Helper()
	names := []string{"LICENSE", "NOTICE", "README.md", binary}
	err := filepath.WalkDir("docs", func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == filepath.Join("docs", "development") {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			names = append(names, filepath.ToSlash(name))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	return names
}

func TestReleaseFilesRecursive(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, name := range []string{"docs/nested/guide.md", "docs/nested/image.png", "docs/development/private.md"} {
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	names, err := releaseFiles("seestar-wormhole")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"LICENSE", "NOTICE", "README.md", "docs/nested/guide.md", "docs/nested/image.png", "seestar-wormhole"}
	if !slices.Equal(names, want) {
		t.Fatalf("release files = %v, want %v", names, want)
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
			cmd := exec.Command(bash, script, tc.version)
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
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

type workflowStep struct {
	Uses, Run string
	Env       map[string]string
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
	if validate[1].Env["RELEASE_VERSION"] != "${{ github.ref_name }}" {
		t.Fatal("validation must receive ref_name through environment immediately after checkout")
	}
	if !slices.Contains(workflow.Jobs["release"].Needs, "validate") {
		t.Fatal("release must need validate")
	}
	release := workflow.Jobs["release"].Steps
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
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "BUILD_MARKER="+marker)
			for key, value := range step.Env {
				if value == "${{ github.ref_name }}" {
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
