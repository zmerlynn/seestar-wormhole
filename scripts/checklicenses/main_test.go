package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	info := &debug.BuildInfo{Deps: []*debug.Module{{Path: "example.com/a"}, {Path: "example.com/b"}}}
	for _, tc := range []struct {
		name, text, reject string
	}{
		{"exact", "NOTICE\n\nModule: example.com/a\nSPDX: MIT\n\nModule: example.com/b\nSPDX: MIT\n", ""},
		{"missing", "NOTICE\n\nModule: example.com/a\nSPDX: MIT\n", "omits linked module example.com/b"},
		{"duplicate", "\nModule: example.com/a\n\nModule: example.com/a\n\nModule: example.com/b\n", "lists example.com/a 2 times"},
		{"not linked", "\nModule: example.com/a\n\nModule: example.com/b\n\nModule: example.com/c\n", "lists example.com/c, which is not linked"},
		{"mention only", "NOTICE names example.com/a and example.com/b\n", "omits linked module example.com/a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := compare(info, tc.text)
			if tc.reject == "" && err != nil || tc.reject != "" && (err == nil || !strings.Contains(err.Error(), tc.reject)) {
				t.Fatalf("compare=%v, want %q", err, tc.reject)
			}
		})
	}
}

// Both shipped CLIs pass when built the way a release builds them.
func TestShippedPrograms(t *testing.T) {
	t.Chdir("../..")
	paths, err := build(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("built %v, want both CLIs", paths)
	}
	if err := run(paths); err != nil {
		t.Fatal(err)
	}
}
