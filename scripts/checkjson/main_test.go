package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		name, file, body string
		reject           bool
	}{
		{"ordinary", "main.go", "package p\nimport \"encoding/json\"", true},
		{"alias and build tag", "main.go", "//go:build ignore\n\npackage p\nimport (j \"encoding/json\")", true},
		{"stdlib experiment", "main.go", "package p\nimport \"encoding/json/jsontext\"", true},
		{"quoted escape", "main.go", "package p\nimport \"encoding/\\u006ason\"", true},
		{"test comparator", "main_test.go", "package p\nimport \"encoding/json\"", false},
		{"v2", "main.go", "package p\nimport \"github.com/go-json-experiment/json\"", false},
		{"comment", "main.go", "package p\n// import \"encoding/json\"", false},
		{"vendor", "vendor/p/main.go", "package p\nimport \"encoding/json\"", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, tc.file)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			if err := check(root); (err != nil) != tc.reject {
				t.Fatalf("check=%v; reject=%t", err, tc.reject)
			}
		})
	}
}
