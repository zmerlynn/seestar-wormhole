//go:build ignore

// Generate the license catalog embedded by internal/licenses.
// Run from the repository root: go run ./scripts/generate-licenses.go [--check]
//
// The modules come from `go list -deps` of the command for every release
// target, so the catalog holds what any release build can link. Each module's
// license and notice files are copied verbatim from vendor/. The NOTICE file
// must contain the module table this generator renders, so the table cannot
// drift from vendor/.
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

const (
	command = "./cmd/seestar-copy-cli"
	dest    = "internal/licenses/embed"
)

var targets = []string{
	"darwin/amd64", "darwin/arm64",
	"linux/amd64", "linux/arm64",
	"windows/amd64", "windows/arm64",
}

// spdx records the license of each linked module, as read from its vendored
// license files. A module added to the build must be added here after its
// license is checked.
var spdx = map[string]string{
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
	for _, name := range []string{"NOTICE", "LICENSE"} {
		if files[name], err = os.ReadFile(name); err != nil {
			return err
		}
	}
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

// linkedModules returns the sorted non-main modules that any release build of
// the command links.
func linkedModules() ([]string, error) {
	seen := map[string]bool{}
	for _, target := range targets {
		goos, goarch, _ := strings.Cut(target, "/")
		cmd := exec.Command("go", "list", "-mod=vendor", "-deps", "-json=Module", command)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("go list for %s: %w", target, err)
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
// blank line that closes it.
func noticeTable(entries []module) string {
	var b strings.Builder
	b.WriteString("| Module | License files under vendor/ | SPDX identifier |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, entry := range entries {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", entry.Path, strings.Join(entry.Files, " and "), entry.SPDX)
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
