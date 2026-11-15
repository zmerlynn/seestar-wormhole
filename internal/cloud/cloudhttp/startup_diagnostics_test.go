//go:build unix

package cloudhttp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
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
	// The error names the directory and keeps the OS error's text.
	var option *diagnostic.OptionError
	if !errors.As(err, &option) || option.Value != file || option.Err == nil || !strings.Contains(err.Error(), file) {
		t.Fatalf("state failure lost the directory or its error: %v", err)
	}
}
