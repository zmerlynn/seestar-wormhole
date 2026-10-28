//go:build guitest

package main

import (
	"debug/buildinfo"
	"fmt"
	"os/exec"
	"strings"
)

// Check both the compiler's selected tags and actual linked symbols. The
// fixture is a positive control so an ineffective scan cannot silently pass.
func verifyIsolation(paths []string) error {
	if len(paths) != 3 {
		return fmt.Errorf("verify requires fixture,production,plain-gui binaries")
	}
	for i, path := range paths {
		info, err := buildinfo.ReadFile(path)
		if err != nil {
			return err
		}
		tags := ""
		for _, s := range info.Settings {
			if s.Key == "-tags" {
				tags = s.Value
			}
		}
		metadata, err := exec.Command("go", "version", "-m", path).CombinedOutput()
		if err != nil {
			return fmt.Errorf("build metadata: %w", err)
		}
		fmt.Print(string(metadata))
		symbols, err := exec.Command("go", "tool", "nm", path).CombinedOutput()
		if err != nil {
			return fmt.Errorf("symbol scan (build without stripping): %w", err)
		}
		found := false
		for _, line := range strings.Split(string(symbols), "\n") {
			if strings.Contains(strings.ToLower(line), "guitest") {
				found = true
				break
			}
		}
		tagged := false
		for _, tag := range strings.Split(tags, ",") {
			if tag == "guitest" {
				tagged = true
			}
		}
		if i == 0 && (!tagged || !found) {
			return fmt.Errorf("fixture positive control missing tag/symbols")
		}
		if i != 0 && (tagged || found) {
			return fmt.Errorf("guitest leaked into %s", path)
		}
		if !strings.Contains(tags, "gui") {
			return fmt.Errorf("missing gui tag: %s", path)
		}
		if i == 1 && !strings.Contains(tags, "production") {
			return fmt.Errorf("missing production tag")
		}
	}
	fmt.Println("SCENARIO build-isolation PASS: fixture positive control; no guitest tags or symbols in production/plain gui")
	return nil
}
