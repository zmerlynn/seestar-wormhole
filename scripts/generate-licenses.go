//go:build ignore

// Generate the embedded third-party notices using Go and the vendored JSON v2 library.
// Run from the repository root: go run ./scripts/generate-licenses.go [--check]
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type module struct {
	Path  string
	SPDX  string
	Files []string
}

func main() {
	check := flag.Bool("check", false, "fail if the embedded notices are stale")
	flag.Parse()
	if err := generate(*check); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func read(path string) ([]byte, error) { return os.ReadFile(filepath.FromSlash(path)) }

func generate(check bool) error {
	modfile, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return err
	}
	if err := os.Chdir(filepath.Dir(strings.TrimSpace(string(modfile)))); err != nil {
		return err
	}
	data, err := read("vendor/modules.txt")
	if err != nil {
		return err
	}
	// modules.txt is Go's vendor manifest: '# path version' starts a module,
	// followed by metadata comments and zero or more package import paths.
	modules := map[string]bool{}
	var current string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "# ") {
			fields := strings.Fields(line)
			if len(fields) != 3 || !strings.HasPrefix(fields[2], "v") {
				return fmt.Errorf("unsupported vendor module header: %s", line)
			}
			current = fields[1]
			modules[current] = false
		} else if line != "" && !strings.HasPrefix(line, "#") {
			if current == "" {
				return errors.New("vendor package without a module header")
			}
			modules[current] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	notice, err := read("NOTICE")
	if err != nil {
		return err
	}
	catalog := map[string]module{}
	for _, line := range strings.Split(string(notice), "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		cols := strings.Split(line, "|")
		if len(cols) != 5 {
			return fmt.Errorf("invalid NOTICE table row: %s", line)
		}
		path, spdx := strings.TrimSpace(cols[1]), strings.TrimSpace(cols[3])
		if path == "Module" || path == "---" {
			continue
		}
		hasPackages, ok := modules[path]
		if !ok {
			return fmt.Errorf("NOTICE module %s is absent from vendor/modules.txt", path)
		}
		if _, duplicate := catalog[path]; duplicate {
			return fmt.Errorf("duplicate NOTICE module %s", path)
		}
		if hasPackages && (spdx == "" || spdx == "see file") {
			return fmt.Errorf("NOTICE module %s needs an SPDX identifier", path)
		}
		if hasPackages {
			for _, file := range strings.Split(strings.TrimSpace(cols[2]), " and ") {
				if !strings.HasPrefix(file, "vendor/"+path+"/") || !fs.ValidPath(file) {
					return fmt.Errorf("invalid NOTICE license path for %s: %s", path, file)
				}
				if _, err := read(file); err != nil {
					return err
				}
			}
		} else if strings.TrimSpace(cols[2]) != "No source or license file copied into vendor/" {
			return fmt.Errorf("NOTICE disagrees with package-free module %s", path)
		}
		catalog[path] = module{Path: path, SPDX: spdx}
	}
	for path := range modules {
		if _, ok := catalog[path]; !ok {
			return fmt.Errorf("vendor module %s is missing from NOTICE", path)
		}
	}

	files := map[string][]byte{"NOTICE": notice}
	var entries []module
	for path, hasPackages := range modules {
		if !hasPackages {
			continue
		}
		entry := catalog[path]
		prefix := "vendor/" + path
		licenseCount := 0
		err := filepath.WalkDir(filepath.FromSlash(prefix), func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name = filepath.ToSlash(name)
			if d.IsDir() {
				if name != prefix {
					if _, nested := modules[strings.TrimPrefix(name, "vendor/")]; nested {
						return filepath.SkipDir
					}
				}
				return nil
			}
			base := strings.ToUpper(d.Name())
			isLicense := strings.HasPrefix(base, "LICENSE") || strings.HasPrefix(base, "LICENCE") || strings.HasPrefix(base, "COPYING")
			if !isLicense && !strings.HasPrefix(base, "NOTICE") && base != "COPYRIGHT" && base != "AUTHORS" {
				return nil
			}
			if isLicense {
				licenseCount++
			}
			content, err := read(name)
			if err != nil {
				return err
			}
			files[name] = content
			entry.Files = append(entry.Files, name)
			return nil
		})
		if err != nil {
			return err
		}
		if licenseCount == 0 {
			return fmt.Errorf("vendored module %s has no license file", path)
		}
		sort.Strings(entry.Files)
		if path == "github.com/pion/dtls/v3" {
			entry.Files = append(entry.Files, "internal/edge/PION-LICENSE")
		}
		for _, name := range entry.Files {
			if _, ok := files[name]; !ok {
				content, err := read(name)
				if err != nil {
					return err
				}
				files[name] = content
			}
		}
		catalog[path] = entry
		entries = append(entries, entry)
	}
	// Let Go resolve the actual target's dependencies, including build tags.
	// Store all vendored notices so the catalog also supports cross-builds;
	// the executable selects its linked modules from debug.ReadBuildInfo.
	cmd := exec.Command("go", "list", "-mod=vendor", "-deps", "-json", "./cmd/seestar-wormhole")
	cmd.Stderr = os.Stderr
	deps, err := cmd.Output()
	if err != nil {
		return err
	}
	decoder := jsontext.NewDecoder(bytes.NewReader(deps))
	for {
		var pkg struct {
			Module *struct {
				Path string
				Main bool
			}
		}
		if err := json.UnmarshalDecode(decoder, &pkg); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if pkg.Module != nil && !pkg.Module.Main && len(catalog[pkg.Module.Path].Files) == 0 {
			return fmt.Errorf("linked module %s has no license files", pkg.Module.Path)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	// Keep manifest indentation, nil lists and HTML/JS escapes unchanged.
	manifest, err := json.Marshal(entries, json.Deterministic(true), json.FormatNilSliceAsNull(true), jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	files["modules.json"] = append(manifest, '\n')
	const dest = "internal/licenses/embed"
	if check {
		seen := map[string]bool{}
		err := filepath.WalkDir(dest, func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			key := strings.TrimPrefix(filepath.ToSlash(name), dest+"/")
			got, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			want, ok := files[key]
			if !ok || !bytes.Equal(got, want) {
				return fmt.Errorf("stale embedded notice: %s; run go run ./scripts/generate-licenses.go", name)
			}
			seen[key] = true
			return nil
		})
		if err != nil {
			return err
		}
		for key := range files {
			if !seen[key] {
				return fmt.Errorf("missing embedded notice: %s", key)
			}
		}
		fmt.Println("embedded licenses are current")
		return nil
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	for name, content := range files {
		path := filepath.Join(dest, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0644); err != nil {
			return err
		}
	}
	fmt.Printf("embedded notices for %d vendored modules\n", len(entries))
	return nil
}
