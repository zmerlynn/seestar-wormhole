package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// proveFile only reads: it calls nothing that removes.
func TestProveFileRemovesNothing(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "delete.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "proveFile" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			var name string
			switch n := n.(type) {
			case *ast.Ident:
				name = n.Name
			case *ast.SelectorExpr:
				name = n.Sel.Name
			}
			if strings.Contains(name, "Remove") || name == "deleteFile" || name == "removeFolder" || name == "rm" {
				t.Errorf("%s: proveFile refers to %s", fset.Position(n.Pos()), name)
			}
			return true
		})
	}
	if !found {
		t.Fatal("proveFile not found")
	}
}
