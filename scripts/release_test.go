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
)

var archiveTime = time.Unix(1793441237, 0).UTC()

func TestReleaseAssetNames(t *testing.T) {
	want := []string{
		"seestar-copy_v0.5.0_darwin_universal.dmg",
		"seestar-copy_v0.5.0_linux_amd64.tar.gz", "seestar-copy_v0.5.0_linux_arm64.tar.gz",
		"seestar-copy_v0.5.0_windows_amd64.zip", "seestar-copy_v0.5.0_windows_arm64.zip",
	}
	if got := expectedAssets("v0.5.0"); !slices.Equal(got, want) {
		t.Fatalf("assets=%v, want %v", got, want)
	}
	if got := archiveName("v0.5.0-rc.1", "windows", "arm64"); got != "seestar-copy_v0.5.0-rc.1_windows_arm64.zip" {
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

func fakeBuild(version, revision, goos, arch, path string) error {
	return os.WriteFile(path, []byte(fmt.Sprintf("%s %s %s %s", version, revision, goos, arch)), 0600)
}

func nativeParts(t *testing.T) {
	t.Helper()
	if err := os.WriteFile("dist/parts/seestar-copy_v0.5.0_darwin_universal.dmg", []byte("synthetic dmg"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseArchivesAndChecksums(t *testing.T) {
	t.Chdir(t.TempDir())
	archiveFixtures(t)
	var built []string
	if err := release("v0.5.0", "revision", archiveTime, func(v, r, goos, arch, path string) error {
		built = append(built, filepath.Base(path))
		return fakeBuild(v, r, goos, arch, path)
	}); err != nil {
		t.Fatal(err)
	}
	wantBuilt := []string{
		"seestar-copy-cli_v0.5.0_linux_amd64", "seestar-copy-cli_v0.5.0_linux_arm64",
		"seestar-copy-cli_v0.5.0_darwin_amd64", "seestar-copy-cli_v0.5.0_darwin_arm64",
		"seestar-copy-cli_v0.5.0_windows_amd64.exe", "seestar-copy-cli_v0.5.0_windows_arm64.exe",
	}
	if !slices.Equal(built, wantBuilt) {
		t.Fatalf("built=%v, want %v", built, wantBuilt)
	}
	// The portable build packages Linux and Windows for local rehearsals, and
	// never publishes a partial checksum file.
	if err := checkInventory("dist/parts", slices.Sorted(slices.Values(wantBuilt))); err != nil {
		t.Fatal(err)
	}
	portable := []string{"seestar-copy_v0.5.0_linux_amd64.tar.gz", "seestar-copy_v0.5.0_linux_arm64.tar.gz", "seestar-copy_v0.5.0_windows_amd64.zip", "seestar-copy_v0.5.0_windows_arm64.zip"}
	for _, name := range portable {
		if _, err := os.Stat(filepath.Join("dist", name)); err != nil {
			t.Fatal(err)
		}
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
	want := []string{"SHA256SUMS", "seestar-copy_v0.5.0_darwin_universal.dmg", "seestar-copy_v0.5.0_linux_amd64.tar.gz", "seestar-copy_v0.5.0_linux_arm64.tar.gz", "seestar-copy_v0.5.0_windows_amd64.zip", "seestar-copy_v0.5.0_windows_arm64.zip"}
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
			part := filepath.Join("parts", assetName("v0.5.0", goos, arch))
			if err := fakeBuild("v0.5.0", "revision", goos, arch, part); err != nil {
				t.Fatal(err)
			}
			name := archiveName("v0.5.0", goos, arch)
			var first []byte
			for run := range 2 {
				// Change source metadata: the archive must ignore it.
				for _, input := range []string{"LICENSE", "NOTICE", part} {
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
			wantNames := []string{"LICENSE", "NOTICE", "seestar-copy-cli" + ext}
			var names []string
			check := func(name string, mode os.FileMode, stamp time.Time, data []byte) {
				t.Helper()
				names = append(names, name)
				wantMode, wantData := os.FileMode(0644), name+" fixture"
				if name == "seestar-copy-cli"+ext {
					wantMode = 0755
					wantData = fmt.Sprintf("v0.5.0 revision %s %s", goos, arch)
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
	err := release("v0.5.0", "revision", archiveTime, func(_, _, _, _, path string) error {
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
	name := "dist/seestar-copy_v0.5.0_windows_amd64.zip"
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
	if _, err := os.Stat("dist/parts/seestar-copy-cli_v0.5.0_linux_amd64"); err != nil {
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
