//go:build release

// Release builds and deterministic archives using only Go's standard library.
// Invoke through release.sh from any directory.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ZIP cannot represent dates before 1980. Use the same UTC epoch for both formats.
var archiveTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

func main() {
	os.Exit(releaseCode(os.Args[1:], os.Stderr))
}

var releaseVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.(0|[1-9][0-9]*))?$`)

func releaseCode(args []string, stderr io.Writer) int {
	if len(args) != 2 || !releaseVersion.MatchString(args[0]) {
		fmt.Fprintln(stderr, "release version must be vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-rc.N, without leading zeros")
		return 2
	}
	if err := release(args[0], args[1]); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func release(version, revision string) error {
	if err := os.MkdirAll("dist", 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp("dist", ".release-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	var names []string
	var sums strings.Builder
	for _, platform := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"} {
		parts := strings.Split(platform, "/")
		binary := "seestar-wormhole"
		ext := ".tar.gz"
		if parts[0] == "windows" {
			binary += ".exe"
			ext = ".zip"
		}
		binaryPath := filepath.Join(stage, binary)
		fmt.Printf("Building %s (%s, %s)\n", platform, version, revision)
		// versionString already supplies parentheses around revision and platform.
		// Keep module build info: licenses relies on debug.ReadBuildInfo, even with -s -w.
		cmd := exec.Command("go", "build", "-mod=vendor", "-trimpath", "-buildvcs=false",
			"-ldflags", "-s -w -X main.version="+version+" -X main.revision="+revision,
			"-o", binaryPath, "./cmd/seestar-wormhole")
		cmd.Env = append(os.Environ(), "GOOS="+parts[0], "GOARCH="+parts[1], "CGO_ENABLED=0")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", platform, err)
		}
		name := "seestar-wormhole_" + version + "_" + parts[0] + "_" + parts[1] + ext
		archivePath := filepath.Join(stage, name)
		if err := archive(archivePath, binaryPath, binary, parts[0] == "windows"); err != nil {
			return err
		}
		if err := os.Remove(binaryPath); err != nil {
			return err
		}
		f, err := os.Open(archivePath)
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
		names = append(names, name)
	}
	if err := os.WriteFile(filepath.Join(stage, "SHA256SUMS"), []byte(sums.String()), 0644); err != nil {
		return err
	}
	// Only expose new output after all six builds and archives have succeeded.
	for _, name := range append(names, "SHA256SUMS") {
		if err := os.Rename(filepath.Join(stage, name), filepath.Join("dist", name)); err != nil {
			return err
		}
	}
	fmt.Print(sums.String())
	return nil
}

func releaseFiles(binary string) ([]string, error) {
	names := []string{"LICENSE", "NOTICE", "README.md", binary}
	err := filepath.WalkDir("docs", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == filepath.Join("docs", "development") {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("release documentation must be a regular file: %s", path)
		}
		names = append(names, filepath.ToSlash(path))
		return nil
	})
	sort.Strings(names)
	return names, err
}

func archive(path, binaryPath, binary string, windows bool) (result error) {
	names, err := releaseFiles(binary)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); result == nil {
			result = err
		}
	}()
	// Fixed order, names, modes, timestamps and ownership; no source file metadata.
	var zw *zip.Writer
	var tw *tar.Writer
	var gz *gzip.Writer
	if windows {
		zw = zip.NewWriter(f)
	} else {
		gz = gzip.NewWriter(f) // Zero timestamp, no original name, OS=255.
		tw = tar.NewWriter(gz)
	}
	for _, name := range names {
		source, mode := name, os.FileMode(0644)
		if name == binary {
			source, mode = binaryPath, 0755
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		var dst io.Writer
		if windows {
			h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: archiveTime}
			h.SetMode(mode)
			dst, err = zw.CreateHeader(h)
		} else {
			err = tw.WriteHeader(&tar.Header{Name: name, Mode: int64(mode), Size: int64(len(data)),
				ModTime: archiveTime, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR})
			dst = tw
		}
		if err != nil {
			return err
		}
		if _, err := dst.Write(data); err != nil {
			return err
		}
	}
	if windows {
		return zw.Close()
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
