//go:build guitest

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailureLogs(t *testing.T) {
	dir := t.TempDir()
	var log strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&log, "line %02d http://example.invalid/#secret\n", i)
	}
	for _, sub := range []string{".", "second", "relaunch"} {
		path := filepath.Join(dir, sub)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "process.log"), []byte(log.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	printFailure(&out, "fresh", dir, errors.New("failed http://example.invalid/#secret"))
	got := out.String()
	if strings.Contains(got, "secret") || strings.Contains(got, "line 09") || strings.Count(got, "line 10") != 3 || strings.Count(got, "line 49") != 3 {
		t.Fatal("missing, unbounded, or unsafe process-log tail:", got)
	}
}
