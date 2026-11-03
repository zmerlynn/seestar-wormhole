//go:build ignore

// Generate the license catalog embedded by internal/licenses.
// Run from the repository root: go run ./scripts/generate-licenses.go [--check]
//
// The modules come from `go list -deps` of both commands for every release
// target, the app with and without its window, so the catalog holds what any
// release build can link. Each module's license and notice files are copied
// verbatim from vendor/, with the supplemental notices under
// internal/licenses/notices/<module>/ for source-file terms upstream keeps
// in no separate file. The NOTICE file must contain the module table this
// generator renders, so the table cannot drift from vendor/. Its Linux
// native GUI section is embedded apart, as native-linux/NOTICE, since only
// the Linux window build links those libraries.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const dest = "internal/licenses/embed"

// builds are the release builds of each target: the CLI without cgo, and
// the app with and without its window. The window uses cgo on macOS and
// Linux.
var builds = []struct{ command, tags string }{
	{"./cmd/seestar-copy-cli", ""},
	{"./cmd/seestar-copy", ""},
	{"./cmd/seestar-copy", "gui,production"},
}

// nativeHeading starts NOTICE's Linux native GUI section.
const nativeHeading = "\nLinux native GUI\n"

var targets = []string{
	"darwin/amd64", "darwin/arm64",
	"linux/amd64", "linux/arm64",
	"windows/amd64", "windows/arm64",
}

// spdx records the license of each linked module, as read from its vendored
// license files. A module added to the build must be added here after its
// license is checked.
var spdx = map[string]string{
	"github.com/adrg/xdg":                  "MIT",
	"github.com/go-ole/go-ole":             "MIT",
	"github.com/godbus/dbus/v5":            "BSD-2-Clause",
	"github.com/wailsapp/wails/v3":         "MIT AND ISC AND BSD-3-Clause AND Apache-2.0",
	"github.com/geoffgarside/ber":          "BSD-3-Clause",
	"github.com/go-crypt/x":                "BSD-3-Clause",
	"github.com/go-krb5/krb5":              "Apache-2.0",
	"github.com/go-krb5/x":                 "BSD-3-Clause AND Apache-2.0",
	"github.com/google/uuid":               "BSD-3-Clause",
	"github.com/hirochachacha/go-smb2/v2":  "BSD-2-Clause",
	"github.com/inconshreveable/mousetrap": "Apache-2.0",
	"github.com/pierrec/lz4/v4":            "BSD-3-Clause",
	"github.com/quic-go/quic-go":           "MIT",
	"github.com/spf13/cobra":               "Apache-2.0",
	"github.com/spf13/pflag":               "BSD-3-Clause",
	"golang.org/x/crypto":                  "BSD-3-Clause",
	"golang.org/x/net":                     "BSD-3-Clause",
	"golang.org/x/sys":                     "BSD-3-Clause",
	"golang.org/x/text":                    "BSD-3-Clause",
}

type module struct {
	Path  string
	SPDX  string
	Files []string
}

func main() {
	check := flag.Bool("check", false, "fail if the embedded catalog is stale")
	flag.Parse()
	if err := generate(*check); err != nil {
		fmt.Fprintln(os.Stderr, "generate-licenses:", err)
		os.Exit(1)
	}
}

func generate(check bool) error {
	modfile, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return err
	}
	if err := os.Chdir(filepath.Dir(strings.TrimSpace(string(modfile)))); err != nil {
		return err
	}
	paths, err := linkedModules()
	if err != nil {
		return err
	}
	for path := range spdx {
		if !slices.Contains(paths, path) {
			return fmt.Errorf("module %s has an SPDX entry but no release build links it", path)
		}
	}
	files := map[string][]byte{}
	if files["LICENSE"], err = os.ReadFile("LICENSE"); err != nil {
		return err
	}
	notice, err := os.ReadFile("NOTICE")
	if err != nil {
		return err
	}
	common, native, ok := bytes.Cut(notice, []byte(nativeHeading))
	if !ok {
		return errors.New("NOTICE lacks the Linux native GUI section")
	}
	files["NOTICE"] = common
	files["native-linux/NOTICE"] = append([]byte(nativeHeading), native...)
	var entries []module
	for _, path := range paths {
		id, ok := spdx[path]
		if !ok {
			return fmt.Errorf("linked module %s has no SPDX entry; check its license and add one", path)
		}
		entry := module{Path: path, SPDX: id}
		if entry.Files, err = licenseFiles(path, paths); err != nil {
			return err
		}
		supplements, err := filepath.Glob(filepath.Join("internal/licenses/notices", filepath.FromSlash(path), "*"))
		if err != nil {
			return err
		}
		for _, name := range supplements {
			entry.Files = append(entry.Files, filepath.ToSlash(name))
		}
		slices.Sort(entry.Files)
		for _, name := range entry.Files {
			if files[name], err = os.ReadFile(filepath.FromSlash(name)); err != nil {
				return err
			}
		}
		entries = append(entries, entry)
	}
	if table := noticeTable(entries); !bytes.Contains(files["NOTICE"], []byte(table)) {
		return fmt.Errorf("NOTICE lacks the current module table:\n%s", table)
	}
	manifest, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	files["modules.json"] = append(manifest, '\n')
	if check {
		return compare(files)
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	for name, content := range files {
		path := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("embedded notices for %d modules\n", len(entries))
	return nil
}

// linkedModules returns the sorted non-main modules that any release build
// links.
func linkedModules() ([]string, error) {
	seen := map[string]bool{}
	for _, target := range targets {
		for _, build := range builds {
			goos, goarch, _ := strings.Cut(target, "/")
			cgo := "0"
			if build.tags != "" && goos != "windows" {
				cgo = "1"
			}
			cmd := exec.Command("go", "list", "-mod=vendor", "-deps", "-json=Module", "-tags="+build.tags, build.command)
			cmd.Env = append(os.Environ(), "CGO_ENABLED="+cgo, "GOOS="+goos, "GOARCH="+goarch)
			cmd.Stderr = os.Stderr
			out, err := cmd.Output()
			if err != nil {
				return nil, fmt.Errorf("go list %s -tags=%q for %s: %w", build.command, build.tags, target, err)
			}
			decoder := json.NewDecoder(bytes.NewReader(out))
			for {
				var pkg struct {
					Module *struct {
						Path string
						Main bool
					}
				}
				if err := decoder.Decode(&pkg); errors.Is(err, io.EOF) {
					break
				} else if err != nil {
					return nil, err
				}
				if pkg.Module != nil && !pkg.Module.Main {
					seen[pkg.Module.Path] = true
				}
			}
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	return paths, nil
}

// licenseFiles lists a module's vendored license and notice files, sorted,
// leaving out modules vendored inside its directory.
func licenseFiles(path string, modules []string) ([]string, error) {
	root := "vendor/" + path
	var names []string
	licenses := 0
	err := filepath.WalkDir(filepath.FromSlash(root), func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		if d.IsDir() {
			if name != root && slices.Contains(modules, strings.TrimPrefix(name, "vendor/")) {
				return filepath.SkipDir
			}
			return nil
		}
		base := strings.ToUpper(d.Name())
		isLicense := strings.HasPrefix(base, "LICENSE") || strings.HasPrefix(base, "LICENCE") || strings.HasPrefix(base, "COPYING")
		if isLicense {
			licenses++
		}
		if isLicense || strings.HasPrefix(base, "NOTICE") || base == "COPYRIGHT" || base == "AUTHORS" {
			names = append(names, name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if licenses == 0 {
		return nil, fmt.Errorf("vendored module %s has no license file", path)
	}
	slices.Sort(names)
	return names, nil
}

// noticeTable renders the module table NOTICE must contain, ending with the
// blank line that closes it. It lists the files under vendor/; NOTICE
// describes the supplemental notices in prose.
func noticeTable(entries []module) string {
	var b strings.Builder
	b.WriteString("| Module | License files under vendor/ | SPDX identifier |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, entry := range entries {
		vendored := slices.DeleteFunc(slices.Clone(entry.Files), func(name string) bool { return !strings.HasPrefix(name, "vendor/") })
		fmt.Fprintf(&b, "| %s | %s | %s |\n", entry.Path, strings.Join(vendored, " and "), entry.SPDX)
	}
	b.WriteString("\n")
	return b.String()
}

// compare fails unless the embed directory holds exactly the generated files.
func compare(files map[string][]byte) error {
	seen := map[string]bool{}
	err := filepath.WalkDir(dest, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		key := strings.TrimPrefix(filepath.ToSlash(name), dest+"/")
		got, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		if want, ok := files[key]; !ok || !bytes.Equal(got, want) {
			return fmt.Errorf("stale embedded notice %s; run go run ./scripts/generate-licenses.go", name)
		}
		seen[key] = true
		return nil
	})
	if err != nil {
		return err
	}
	for key := range files {
		if !seen[key] {
			return fmt.Errorf("missing embedded notice %s; run go run ./scripts/generate-licenses.go", key)
		}
	}
	fmt.Println("embedded licenses are current")
	return nil
}
