// Command checkjson prevents application and tooling code from returning to
// encoding/json, whose decoder accepts ambiguous duplicate member names.
package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func check(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && (entry.Name() == "vendor" || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if name == "encoding/json/v2" || name == "encoding/json/jsontext" {
				continue
			}
			if name == "encoding/json" || strings.HasPrefix(name, "encoding/json/") ||
				name == "github.com/go-json-experiment/json" || strings.HasPrefix(name, "github.com/go-json-experiment/json/") {
				return fmt.Errorf("%s: import %q: use encoding/json/v2 or encoding/json/jsontext", path, name)
			}
		}
		return nil
	})
}
func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	if err := check(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
