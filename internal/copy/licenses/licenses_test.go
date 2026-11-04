package licenses

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
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

// noticeParts splits the repository's NOTICE into its common part, the
// Linux native GUI section and the app's module texts, which are not
// embedded.
func noticeParts(t *testing.T) (common, native, app string) {
	t.Helper()
	head, app, ok := strings.Cut(string(repoFile(t, "NOTICE")), "\nModules only seestar-copy links\n")
	if !ok {
		t.Fatal("NOTICE lacks the section of the app's module texts")
	}
	common, native, ok = strings.Cut(head, "\nLinux native GUI\n")
	if !ok {
		t.Fatal("NOTICE lacks the Linux native GUI section")
	}
	return common, "\nLinux native GUI\n" + native, app
}

func TestOnlyLinkedModules(t *testing.T) {
	var out bytes.Buffer
	info := &debug.BuildInfo{Deps: []*debug.Module{{Path: "github.com/spf13/pflag"}}}
	if err := writeForBuild(&out, info); err != nil {
		t.Fatal(err)
	}
	notice, _, _ := noticeParts(t)
	license, pflag := repoFile(t, "LICENSE"), repoFile(t, "vendor/github.com/spf13/pflag/LICENSE")
	want := notice + "\n--- LICENSE ---\n" + string(license) +
		"\nModule: github.com/spf13/pflag\nSPDX: BSD-3-Clause\n\n--- vendor/github.com/spf13/pflag/LICENSE ---\n" + string(pflag)
	if got := out.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// The GTK and WebKitGTK notice is printed by the Linux window build only,
// which is the one build that links them: a Linux cgo build that links
// Wails. Tags alone don't select it.
func TestNativeLinuxNotice(t *testing.T) {
	_, native, _ := noticeParts(t)
	const wails = "github.com/wailsapp/wails/v3"
	for _, c := range []struct {
		goos, cgo, tags, dep string
		want                 bool
	}{
		{"linux", "1", "gui,production", wails, true},
		{"linux", "1", "", wails, true},
		{"linux", "1", "gui,production", "github.com/spf13/pflag", false},
		{"linux", "0", "gui,production", wails, false},
		{"darwin", "1", "gui,production", wails, false},
		{"windows", "0", "gui,production", wails, false},
	} {
		info := &debug.BuildInfo{
			Deps:     []*debug.Module{{Path: c.dep}},
			Settings: []debug.BuildSetting{{Key: "GOOS", Value: c.goos}, {Key: "CGO_ENABLED", Value: c.cgo}},
		}
		if c.tags != "" {
			info.Settings = append(info.Settings, debug.BuildSetting{Key: "-tags", Value: c.tags})
		}
		var out bytes.Buffer
		if err := writeForBuild(&out, info); err != nil {
			t.Fatal(err)
		}
		if got := strings.HasSuffix(out.String(), native); got != c.want || strings.Contains(strings.TrimSuffix(out.String(), native), "LGPL") {
			t.Errorf("%+v: native notice at the end %t, want %t", c, got, c.want)
		}
	}
}

// windowsBuildInfo cross-builds a command for windows/amd64 without cgo and
// returns its build information.
func windowsBuildInfo(t *testing.T, tags, command string) *debug.BuildInfo {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "program.exe")
	cmd := exec.Command("go", "build", "-mod=vendor", "-trimpath", "-buildvcs=false", "-tags="+tags, "-ldflags=-s -w", "-o", binary, command)
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// The window build for Windows, the one cross-built without cgo, prints
// Wails' notices with its supplements.
func TestWindowNotices(t *testing.T) {
	info := windowsBuildInfo(t, "gui,production", "./cmd/seestar-copy")
	var out bytes.Buffer
	if err := writeForBuild(&out, info); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\nModule: github.com/wailsapp/wails/v3\nSPDX: MIT AND ISC AND BSD-3-Clause AND Apache-2.0\n\n--- internal/licenses/notices/github.com/wailsapp/wails/v3/fyne-systray.txt ---\n",
		"\n--- internal/licenses/notices/github.com/wailsapp/wails/v3/pkg-w32-clipboard.go.txt ---\n",
		"\n--- vendor/github.com/wailsapp/wails/v3/LICENSE ---\n",
		"\nModule: github.com/go-ole/go-ole\nSPDX: MIT\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q", want)
		}
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
	common, native, _ := noticeParts(t)
	for name, want := range map[string]string{"NOTICE": common, "native-linux/NOTICE": native, "LICENSE": string(repoFile(t, "LICENSE"))} {
		got, err := notices.ReadFile("embed/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("embedded %s differs from the repository's; run go run ./scripts/generate-licenses.go", name)
		}
	}
}

// The app prints no notices, so NOTICE's last section carries the full texts
// of each module the Windows window build links and the CLI does not.
func TestAppOnlyModuleTexts(t *testing.T) {
	_, _, app := noticeParts(t)
	cli := map[string]bool{}
	for _, dep := range windowsBuildInfo(t, "", "./cmd/seestar-copy-cli").Deps {
		cli[dep.Path] = true
	}
	manifest, err := notices.ReadFile("embed/modules.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []module
	if err := json.Unmarshal(manifest, &entries); err != nil {
		t.Fatal(err)
	}
	files := map[string][]string{}
	for _, entry := range entries {
		files[entry.Path] = entry.Files
	}
	checked := 0
	for _, dep := range windowsBuildInfo(t, "gui,production", "./cmd/seestar-copy").Deps {
		if cli[dep.Path] {
			if strings.Contains(app, "\nModule: "+dep.Path+"\n") {
				t.Errorf("NOTICE's app section has %s, which the CLI links", dep.Path)
			}
			continue
		}
		if !strings.Contains(app, "\nModule: "+dep.Path+"\n") {
			t.Errorf("NOTICE's app section lacks %s", dep.Path)
		}
		for _, name := range files[dep.Path] {
			if want := "\n--- " + name + " ---\n" + string(repoFile(t, name)); !strings.Contains(app, want) {
				t.Errorf("NOTICE's app section lacks the text of %s", name)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Error("no app-only module texts checked")
	}
}
