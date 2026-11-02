package licenses

import (
	"bytes"
	"debug/buildinfo"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func repoFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../..", filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Build each release target without cgo and run the notice writer on its
// module metadata. Foreign executables are inspected, never run.
func TestTargetNotices(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(goos+"/"+arch, func(t *testing.T) {
				binary := filepath.Join(t.TempDir(), "seestar-copy-cli")
				cmd := exec.Command("go", "build", "-mod=vendor", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", binary, "./cmd/seestar-copy-cli")
				cmd.Dir = "../.."
				cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+arch)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("build: %v\n%s", err, out)
				}
				info, err := buildinfo.ReadFile(binary)
				if err != nil {
					t.Fatal(err)
				}
				var out bytes.Buffer
				if err := writeForBuild(&out, info); err != nil {
					t.Fatal(err)
				}
				text := out.String()
				for _, want := range []string{
					"\nModule: github.com/spf13/cobra\nSPDX: Apache-2.0\n\n--- vendor/github.com/spf13/cobra/LICENSE.txt ---\n",
					"\nModule: github.com/spf13/pflag\nSPDX: BSD-3-Clause\n\n--- vendor/github.com/spf13/pflag/LICENSE ---\n",
				} {
					if !strings.Contains(text, want) {
						t.Errorf("missing %q", want)
					}
				}
				mousetrap := strings.Contains(text, "\nModule: github.com/inconshreveable/mousetrap\nSPDX: Apache-2.0\n")
				if mousetrap != (goos == "windows") {
					t.Errorf("mousetrap notice present=%t, want it on Windows only", mousetrap)
				}
				for _, dep := range info.Deps {
					if n := strings.Count(text, "\nModule: "+dep.Path+"\n"); n != 1 {
						t.Errorf("module %s printed %d times, want once", dep.Path, n)
					}
				}
			})
		}
	}
}

func TestOnlyLinkedModules(t *testing.T) {
	var out bytes.Buffer
	info := &debug.BuildInfo{Deps: []*debug.Module{{Path: "github.com/spf13/pflag"}}}
	if err := writeForBuild(&out, info); err != nil {
		t.Fatal(err)
	}
	notice, license, pflag := repoFile(t, "NOTICE"), repoFile(t, "LICENSE"), repoFile(t, "vendor/github.com/spf13/pflag/LICENSE")
	want := string(notice) + "\n--- LICENSE ---\n" + string(license) +
		"\nModule: github.com/spf13/pflag\nSPDX: BSD-3-Clause\n\n--- vendor/github.com/spf13/pflag/LICENSE ---\n" + string(pflag)
	if got := out.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestUnknownLinkedModule(t *testing.T) {
	var out bytes.Buffer
	info := &debug.BuildInfo{Deps: []*debug.Module{{Path: "github.com/spf13/pflag"}, {Path: "example.com/unlisted"}}}
	err := writeForBuild(&out, info)
	if err == nil || err.Error() != "licenses: no embedded license for linked module example.com/unlisted" {
		t.Fatalf("err=%v, want the unlisted module named", err)
	}
	if out.Len() != 0 {
		t.Errorf("printed %d bytes before failing, want none", out.Len())
	}
}

func TestEmbeddedProjectFiles(t *testing.T) {
	for _, name := range []string{"NOTICE", "LICENSE"} {
		got, err := notices.ReadFile("embed/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, repoFile(t, name)) {
			t.Errorf("embedded %s differs from the repository's; run go run ./scripts/generate-licenses.go", name)
		}
	}
}
