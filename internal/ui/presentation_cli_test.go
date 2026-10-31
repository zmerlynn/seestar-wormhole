//go:build !gui

package ui

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCLICannotStartUI(t *testing.T) {
	root := filepath.Join(t.TempDir(), "unused")
	if err := Run(t.Context(), root, io.Discard, nil, "test"); err == nil {
		t.Fatal("CLI started UI")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("CLI touched UI storage: %v", err)
	}
}
