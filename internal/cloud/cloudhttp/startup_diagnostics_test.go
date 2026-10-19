//go:build unix

package cloudhttp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestMissingStateDirectoryAndDiagnostic(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".live-state")
	if _, err := persistentSource(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("state directory not private")
	}
	file := filepath.Join(root, "sentinel-password")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = persistentSource(file)
	if err == nil || !strings.Contains(err.Error(), "state_directory: not_a_directory") {
		t.Fatalf("unclassified state failure: %v", err)
	}
	if !errors.Is(err, cloud.ErrConfig) {
		t.Fatal("state failure lost configuration classification")
	}
	var visit func(error)
	visit = func(e error) {
		if e == nil {
			return
		}
		if strings.Contains(e.Error(), "sentinel-password") {
			t.Errorf("path leaked through %T: %v", e, e)
		}
		switch e := e.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range e.Unwrap() {
				visit(child)
			}
		case interface{ Unwrap() error }:
			visit(e.Unwrap())
		}
	}
	visit(err)
}
