package cloudhttp

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

func TestSourceChild(t *testing.T) {
	if os.Getenv("FLAG_TRIM_SOURCE_CHILD") != "1" {
		return
	}
	id, err := persistentSource(os.Getenv("FLAG_TRIM_SOURCE_PATH"))
	if err != nil {
		os.Exit(2)
	}
	os.Stdout.WriteString(id)
	os.Exit(0)
}

func TestInstallationIdentityAcrossProcesses(t *testing.T) {
	for _, stored := range []bool{false, true} {
		t.Run(map[bool]string{false: "ephemeral", true: "stored"}[stored], func(t *testing.T) {
			root := t.TempDir()
			sentinel := filepath.Join(root, "sentinel")
			if err := os.WriteFile(sentinel, []byte("synthetic sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			state := ""
			if stored {
				state = filepath.Join(root, "state")
			}
			child := func() []byte {
				cmd := exec.Command(os.Args[0], "-test.run=^TestSourceChild$")
				cmd.Env = append(os.Environ(), "FLAG_TRIM_SOURCE_CHILD=1", "FLAG_TRIM_SOURCE_PATH="+state, "HOME="+root, "XDG_CONFIG_HOME="+root)
				out, err := cmd.Output()
				if err != nil {
					t.Fatal("identity child failed", err)
				}
				if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).Match(out) {
					t.Fatal("invalid UUID shape")
				}
				return out
			}
			a, b := child(), child()
			if bytes.Equal(a, b) != stored {
				t.Fatal("identity persistence disagrees with storage mode")
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if !stored && (len(entries) != 1 || entries[0].Name() != "sentinel") {
				t.Fatal("ephemeral identity wrote filesystem")
			}
			data, err := os.ReadFile(sentinel)
			if err != nil || string(data) != "synthetic sentinel" {
				t.Fatal("sentinel changed")
			}
			if stored {
				data, err := os.ReadFile(filepath.Join(state, "InterProcessKV", "device_uuid"))
				if err != nil || !bytes.Equal(data, a) {
					t.Fatal("stored identity mismatch")
				}
			}
		})
	}
}

func TestEphemeralSourceWithoutConfigHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	a, err := persistentSource("")
	if err != nil {
		t.Fatal(err)
	}
	b, err := persistentSource("")
	if err != nil || a == b {
		t.Fatal("ephemeral identity reused or needs config home")
	}
}
