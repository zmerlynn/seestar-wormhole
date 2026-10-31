//go:build release

// Release builds bare CLI and Windows UI executables using Go's standard library.
// Invoke through release.sh from any directory.
package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

func main() { os.Exit(releaseCode(os.Args[1:], os.Stderr)) }

var releaseVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.(0|[1-9][0-9]*))?$`)
var platforms = []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"}

func releaseCode(args []string, stderr io.Writer) int {
	if (len(args) != 2 && !(len(args) == 3 && args[2] == "checksums")) || !releaseVersion.MatchString(args[0]) {
		fmt.Fprintln(stderr, "release version must be vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N, without leading zeros")
		return 2
	}
	var err error
	if len(args) == 3 {
		err = finalChecksums(args[0])
	} else {
		err = release(args[0], args[1], buildBinary)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func assetName(version, goos, arch string, gui bool) string {
	program, ext := "seestar-wormhole-cli", ""
	if gui {
		program = "seestar-wormhole"
	}
	if goos == "windows" {
		ext = ".exe"
	}
	if gui && goos == "darwin" {
		ext = ".dmg"
	}
	return program + "_" + version + "_" + goos + "_" + arch + ext
}

func expectedAssets(version string) []string {
	var names []string
	for _, platform := range platforms {
		parts := strings.Split(platform, "/")
		names = append(names, assetName(version, parts[0], parts[1], false))
		if parts[0] != "darwin" {
			names = append(names, assetName(version, parts[0], parts[1], true))
		}
	}
	names = append(names, assetName(version, "darwin", "universal", true))
	slices.Sort(names)
	return names
}

func buildBinary(version, revision, goos, arch string, gui bool, path string) error {
	fmt.Printf("Building %s/%s gui=%t (%s, %s)\n", goos, arch, gui, version, revision)
	// Keep module build info: licenses relies on debug.ReadBuildInfo even with -s -w.
	flags := "-s -w -X main.version=" + version + " -X main.revision=" + revision
	args := []string{"build", "-mod=vendor", "-trimpath", "-buildvcs=false"}
	if gui {
		args = append(args, "-tags=gui,production")
		flags += " -H windowsgui"
	}
	args = append(args, "-ldflags", flags, "-o", path, "./cmd/seestar-wormhole")
	cmd := exec.Command("go", args...)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+arch, "CGO_ENABLED=0")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func release(version, revision string, build func(string, string, string, string, bool, string) error) error {
	if err := os.MkdirAll("dist", 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp("dist", ".release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	var names []string
	for _, platform := range platforms {
		parts := strings.Split(platform, "/")
		variants := []bool{false}
		if parts[0] == "windows" {
			variants = append(variants, true)
		}
		for _, gui := range variants {
			name := assetName(version, parts[0], parts[1], gui)
			if err := build(version, revision, parts[0], parts[1], gui, filepath.Join(stage, name)); err != nil {
				return fmt.Errorf("build %s: %w", name, err)
			}
			names = append(names, name)
		}
	}
	if err := writeChecksums(stage, names); err != nil {
		return err
	}
	// Publish only after all eight builds succeed. Native UI jobs supply the rest.
	for _, name := range append(names, "SHA256SUMS") {
		if err := os.Rename(filepath.Join(stage, name), filepath.Join("dist", name)); err != nil {
			return err
		}
	}
	return nil
}

func writeChecksums(dir string, names []string) error {
	names = slices.Clone(names)
	slices.Sort(names)
	var sums strings.Builder
	for _, name := range names {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		fmt.Fprintf(&sums, "%x  %s\n", hash.Sum(nil), name)
	}
	return os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0644)
}

func finalChecksums(version string) error {
	entries, err := os.ReadDir("dist")
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if entry.Name() == "SHA256SUMS" {
			continue
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular release asset: %s", entry.Name())
		}
		names = append(names, entry.Name())
	}
	want := expectedAssets(version)
	if !slices.Equal(names, want) {
		return fmt.Errorf("expected eleven release assets: got %v, want %v", names, want)
	}
	return writeChecksums("dist", names)
}
