//go:build release

// Release builds the CLI for every platform and the Windows app, and writes
// deterministic archives. Invoke through release.sh from any directory.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/flate"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

func main() { os.Exit(releaseCode(os.Args[1:], os.Stderr)) }

var releaseVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.(0|[1-9][0-9]*))?$`)
var platforms = []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"}

// The archives built from parts; the DMG comes from the Mac job. The Linux
// app needs cgo, so only the final job, with the native parts, packs Linux.
var archived = [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"}}
var portable = [][2]string{{"windows", "amd64"}, {"windows", "arm64"}}

func releaseCode(args []string, stderr io.Writer) int {
	if (len(args) != 2 && !(len(args) == 3 && args[2] == "checksums")) || !releaseVersion.MatchString(args[0]) {
		fmt.Fprintln(stderr, "release version must be vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N, without leading zeros")
		return 2
	}
	stamp, err := exec.Command("git", "show", "-s", "--format=%ct", args[1]).Output()
	if err == nil {
		var seconds int64
		seconds, err = strconv.ParseInt(strings.TrimSpace(string(stamp)), 10, 64)
		if err == nil {
			mtime := time.Unix(seconds, 0).UTC()
			if len(args) == 3 {
				err = finalChecksums(args[0], mtime)
			} else {
				err = release(args[0], args[1], mtime, buildBinary)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// program names the CLI or the app executable inside a download.
func program(goos string, app bool) string {
	name := "seestar-copy-cli"
	if app {
		name = "seestar-copy"
	}
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// assetName names one built executable in dist/parts.
func assetName(version, goos, arch string, app bool) string {
	name := program(goos, app)
	base := strings.TrimSuffix(name, ".exe")
	return base + "_" + version + "_" + goos + "_" + arch + name[len(base):]
}

func archiveName(version, goos, arch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	if goos == "darwin" {
		ext = ".dmg"
	}
	return "seestar-copy_" + version + "_" + goos + "_" + arch + ext
}

func expectedAssets(version string) []string {
	var names []string
	for _, p := range archived {
		names = append(names, archiveName(version, p[0], p[1]))
	}
	names = append(names, archiveName(version, "darwin", "universal"))
	slices.Sort(names)
	return names
}

func buildBinary(version, revision, goos, arch string, app bool, path string) error {
	fmt.Printf("Building %s/%s app=%t (%s, %s)\n", goos, arch, app, version, revision)
	cmd := buildCommand(version, revision, goos, arch, app, path)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// buildCommand is the go build for one part. The app is the Windows window
// build: no cgo, no console window, and no revision, which it does not show.
func buildCommand(version, revision, goos, arch string, app bool, path string) *exec.Cmd {
	// Keep module build info: licenses relies on debug.ReadBuildInfo even with -s -w.
	args := []string{"build", "-mod=vendor", "-trimpath", "-buildvcs=false"}
	flags, pkg := "-s -w -X main.version="+version+" -X main.revision="+revision, "./cmd/seestar-copy-cli"
	if app {
		args = append(args, "-tags=gui,production")
		flags, pkg = "-s -w -X main.version="+version+" -H windowsgui", "./cmd/seestar-copy"
	}
	cmd := exec.Command("go", append(args, "-ldflags", flags, "-o", path, pkg)...)
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+arch, "CGO_ENABLED=0")
	return cmd
}

func release(version, revision string, mtime time.Time, build func(string, string, string, string, bool, string) error) error {
	if err := os.MkdirAll("dist", 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp("dist", ".release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := os.Mkdir(filepath.Join(stage, "parts"), 0755); err != nil {
		return err
	}
	var names []string
	for _, platform := range platforms {
		goos, arch, _ := strings.Cut(platform, "/")
		for _, app := range []bool{false, true} {
			if app && goos != "windows" {
				continue
			}
			name := assetName(version, goos, arch, app)
			if err := build(version, revision, goos, arch, app, filepath.Join(stage, "parts", name)); err != nil {
				return fmt.Errorf("build %s: %w", name, err)
			}
			names = append(names, name)
		}
	}
	// Package the Windows archives for local portable rehearsals. CI transfers
	// only parts, and the final job assembles the archives again.
	for _, p := range portable {
		if err := writeArchive(stage, filepath.Join(stage, "parts"), version, p[0], p[1], mtime); err != nil {
			return err
		}
	}
	// Publish only after every build and archive succeeds.
	if err := os.MkdirAll("dist/parts", 0755); err != nil {
		return err
	}
	for _, name := range names {
		if err := os.Rename(filepath.Join(stage, "parts", name), filepath.Join("dist/parts", name)); err != nil {
			return err
		}
	}
	for _, p := range portable {
		name := archiveName(version, p[0], p[1])
		if err := os.Rename(filepath.Join(stage, name), filepath.Join("dist", name)); err != nil {
			return err
		}
	}
	return nil
}

// Headers are independent of the input files' ownership, modes and timestamps.
func writeArchive(dir, parts, version, goos, arch string, mtime time.Time) (err error) {
	cli, app := program(goos, false), program(goos, true)
	members := []string{"LICENSE", "NOTICE", cli, app}
	slices.Sort(members)
	f, err := os.Create(filepath.Join(dir, archiveName(version, goos, arch)))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	var tw *tar.Writer
	var zw *zip.Writer
	if goos == "windows" {
		zw = zip.NewWriter(f)
		zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) { return flate.NewWriter(w, flate.BestCompression) })
		defer func() { err = errors.Join(err, zw.Close()) }()
	} else {
		gz, e := gzip.NewWriterLevel(f, gzip.BestCompression)
		if e != nil {
			return e
		}
		defer func() { err = errors.Join(err, gz.Close()) }()
		tw = tar.NewWriter(gz)
		defer func() { err = errors.Join(err, tw.Close()) }()
	}
	for _, name := range members {
		path, mode := name, int64(0644)
		if name == cli || name == app {
			path = filepath.Join(parts, assetName(version, goos, arch, name == app))
			mode = 0755
		}
		data, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		var w io.Writer
		if zw != nil {
			header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: mtime}
			header.SetMode(os.FileMode(mode))
			w, err = zw.CreateHeader(header)
		} else {
			err = tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), ModTime: mtime, Typeflag: tar.TypeReg, Format: tar.FormatPAX})
			w = tw
		}
		if err != nil {
			return err
		}
		if _, err = w.Write(data); err != nil {
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

// Final assembly accepts exactly the builder parts and publishes only the five
// downloads plus SHA256SUMS. The raw Darwin CLIs were consumed by the Mac job,
// which built the Mac app itself.
func finalChecksums(version string, mtime time.Time) error {
	var wantParts []string
	for _, platform := range platforms {
		goos, arch, _ := strings.Cut(platform, "/")
		wantParts = append(wantParts, assetName(version, goos, arch, false))
		if goos != "darwin" {
			wantParts = append(wantParts, assetName(version, goos, arch, true))
		}
	}
	dmg := archiveName(version, "darwin", "universal")
	wantParts = append(wantParts, dmg)
	slices.Sort(wantParts)
	if err := checkInventory("dist/parts", wantParts); err != nil {
		return err
	}
	// Refuse stray root files before mutating any assembled downloads.
	entries, err := os.ReadDir("dist")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "parts" {
			continue
		}
		if !entry.Type().IsRegular() || (entry.Name() != "SHA256SUMS" && !slices.Contains(expectedAssets(version), entry.Name())) {
			return fmt.Errorf("unexpected release asset: %s", entry.Name())
		}
	}
	stage, err := os.MkdirTemp("dist", ".release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	for _, p := range archived {
		if err := writeArchive(stage, "dist/parts", version, p[0], p[1], mtime); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(filepath.Join("dist/parts", dmg))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, dmg), data, 0644); err != nil {
		return err
	}
	names := expectedAssets(version)
	if err := checkInventory(stage, names); err != nil {
		return err
	}
	if err := writeChecksums(stage, names); err != nil {
		return err
	}
	for _, name := range append(names, "SHA256SUMS") {
		if err := os.Rename(filepath.Join(stage, name), filepath.Join("dist", name)); err != nil {
			return err
		}
	}
	if err := os.RemoveAll("dist/parts"); err != nil {
		return err
	}
	if err := os.Remove(stage); err != nil {
		return err
	}
	names = append(expectedAssets(version), "SHA256SUMS")
	slices.Sort(names)
	return checkInventory("dist", names)
}

func checkInventory(dir string, want []string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular release asset: %s", entry.Name())
		}
		names = append(names, entry.Name())
	}
	if !slices.Equal(names, want) {
		return fmt.Errorf("release inventory: got %v, want %v", names, want)
	}
	return nil
}
