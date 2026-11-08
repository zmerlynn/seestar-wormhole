// Command checklicenses runs each executable's licenses command and compares
// the modules it lists with the modules Go recorded in that executable, so a
// linked module cannot ship without its notice. With no arguments it builds
// this platform's cgo-free programs with the release settings and checks them.
package main

import (
	"bytes"
	"debug/buildinfo"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
)

// programs are the cgo-free executables in every release archive.
var programs = []struct{ name, pkg string }{
	{"seestar-wormhole-cli", "./cmd/seestar-wormhole"},
}

func build(dir string) ([]string, error) {
	var paths []string
	for _, p := range programs {
		path := filepath.Join(dir, p.name)
		if runtime.GOOS == "windows" {
			path += ".exe"
		}
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", path, p.pkg)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("build %s: %v\n%s", p.name, err, out)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func checkExecutable(path string) error {
	if runtime.GOOS != "windows" {
		stat, err := os.Stat(path)
		if err != nil {
			return err
		}
		if stat.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("%s is not executable (mode %v); restore its execute permission, which artifact transfer drops", path, stat.Mode().Perm())
		}
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return err
	}
	cmd := exec.Command(path, "licenses")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil || stderr.Len() != 0 {
		return fmt.Errorf("%s licenses: %v; stderr=%s", path, err, &stderr)
	}
	if err := compare(info, stdout.String()); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// compare requires each module in the build information to be listed exactly
// once, and nothing else.
func compare(info *debug.BuildInfo, text string) error {
	listed := map[string]int{}
	for line := range strings.Lines(text) {
		if path, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), "Module: "); ok {
			listed[path]++
		}
	}
	var problems []string
	for _, dep := range info.Deps {
		switch n := listed[dep.Path]; {
		case n == 0:
			problems = append(problems, "licenses omits linked module "+dep.Path)
		case n > 1:
			problems = append(problems, fmt.Sprintf("licenses lists %s %d times", dep.Path, n))
		}
		delete(listed, dep.Path)
	}
	for path := range listed {
		problems = append(problems, "licenses lists "+path+", which is not linked")
	}
	if len(problems) == 0 {
		return nil
	}
	slices.Sort(problems)
	return errors.New(strings.Join(problems, "; "))
}

func run(paths []string) error {
	if len(paths) == 0 {
		dir, err := os.MkdirTemp("", "checklicenses")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		if paths, err = build(dir); err != nil {
			return err
		}
	}
	for _, path := range paths {
		if err := checkExecutable(path); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
