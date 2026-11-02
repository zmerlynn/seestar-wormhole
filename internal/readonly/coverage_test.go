package readonly

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// writeNames are the functions and methods that change the local filesystem,
// matched by name on any receiver or package: the os functions and the
// os.Root and os.File methods that create, open for writing, write at an
// offset, truncate, sync, rename, remove, link or change times, modes or
// owners. A plain Write is not matched, since hashes and buffers have one
// too; an os.File can only be written after one of these opened it for
// writing, and WriteAt, Truncate and Sync are matched.
var writeNames = map[string]bool{
	"Chmod": true, "Chown": true, "Chtimes": true, "Create": true, "CreateTemp": true,
	"Lchown": true, "Link": true, "Mkdir": true, "MkdirAll": true, "MkdirTemp": true,
	"OpenFile": true, "Remove": true, "RemoveAll": true, "Rename": true, "Symlink": true,
	"Sync": true, "Truncate": true, "WriteAt": true, "WriteFile": true,
}

// allowed lists functions, as "<dir>.<name>" with the directory relative to
// the repository root, that change the local filesystem without calling
// Check, each with the reason. It is empty: every such function is guarded.
// scripts/ is outside the scan, and so are _test.go files and vendor/.
var allowed = map[string]string{}

// TestWritesAreGuarded checks, without type information, the rule this
// package rests on: every function under internal/ and cmd/ whose body
// (including function literals in it) refers to a name in writeNames, or
// calls a package-level variable initialized with one (a test seam such as
// syncFile = (*os.File).Sync), starts with a call to readonly.Check. It also
// checks that only tests call Reset.
func TestWritesAreGuarded(t *testing.T) {
	var guarded []string
	for _, top := range []string{"internal", "cmd"} {
		dirs := map[string][]*ast.File{}
		fset := token.NewFileSet()
		err := filepath.WalkDir(filepath.Join("..", "..", top), func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if de.IsDir() {
				if de.Name() == "testdata" || de.Name() == "vendor" {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			dirs[filepath.Dir(p)] = append(dirs[filepath.Dir(p)], f)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for dir, files := range dirs {
			rel := filepath.ToSlash(strings.TrimPrefix(dir, filepath.Join("..", "..")+string(filepath.Separator)))
			seams := seamVars(files)
			check := func(name string, body *ast.BlockStmt, pos token.Pos) {
				if body == nil || !writes(body, seams) {
					return
				}
				key := rel + "." + name
				guarded = append(guarded, key)
				if _, ok := allowed[key]; ok {
					return
				}
				if len(body.List) == 0 || !isCheck(body.List[0]) {
					t.Errorf("%s: %s changes the local filesystem but does not start with readonly.Check", fset.Position(pos), name)
				}
			}
			for _, f := range files {
				for _, decl := range f.Decls {
					switch decl := decl.(type) {
					case *ast.FuncDecl:
						check(decl.Name.Name, decl.Body, decl.Pos())
					case *ast.GenDecl:
						for _, spec := range decl.Specs {
							vs, ok := spec.(*ast.ValueSpec)
							if !ok {
								continue
							}
							for i, v := range vs.Values {
								if lit, ok := v.(*ast.FuncLit); ok {
									check(vs.Names[i].Name, lit.Body, lit.Pos())
								}
							}
						}
					}
				}
				if rel != "internal/readonly" {
					ast.Inspect(f, func(n ast.Node) bool {
						if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Reset" && isIdent(sel.X, "readonly") {
							t.Errorf("%s: readonly.Reset outside a test", fset.Position(sel.Pos()))
						}
						return true
					})
				}
			}
		}
	}
	for key := range allowed {
		if !slices.Contains(guarded, key) {
			t.Errorf("allowed lists %s, which no longer changes the local filesystem", key)
		}
	}
	// The scan must see the writes it is meant to catch.
	for _, want := range []string{
		"internal/state.Open", "internal/state.create", "internal/state.lockFile", "internal/state.Append",
		"internal/state.syncDir", "internal/copier.removeTemps", "internal/copier.download", "internal/copier.syncFolder",
	} {
		if !slices.Contains(guarded, want) {
			t.Errorf("the scan did not find the writes in %s; found %q", want, guarded)
		}
	}
}

// seamVars returns the package-level variables whose initializer refers to
// a write, outside a function literal.
func seamVars(files []*ast.File) map[string]bool {
	seams := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, v := range vs.Values {
					if _, ok := v.(*ast.FuncLit); !ok && writes(v, nil) {
						seams[vs.Names[i].Name] = true
					}
				}
			}
		}
	}
	return seams
}

// writes reports whether n refers to a name in writeNames or calls a seam.
func writes(n ast.Node, seams map[string]bool) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			found = found || writeNames[n.Sel.Name]
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && seams[id.Name] {
				found = true
			}
		}
		return !found
	})
	return found
}

// isCheck reports whether s is a call readonly.Check("...").
func isCheck(s ast.Stmt) bool {
	es, ok := s.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Check" || !isIdent(sel.X, "readonly") {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	op, err := strconv.Unquote(lit.Value)
	return err == nil && op != ""
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}
