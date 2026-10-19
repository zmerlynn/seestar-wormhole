//go:build unix

package main

import (
	"bytes"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"os"
	"path/filepath"
	"testing"
)

func TestStatePaths(t *testing.T) {
	for _, kind := range []string{"explicit-file", "directory-mode", "identity-mode", "identity-symlink", "identity-dir", "identity-invalid", "identity-oversize", "identity-valid"} {
		t.Run(kind, func(t *testing.T) {
			setValidCommandEnvironment(t)
			t.Setenv("SEESTAR_EMAIL", "test@example.invalid")
			t.Setenv("SEESTAR_PASSWORD", "PRIVATE_PASSWORD")
			root := filepath.Join(t.TempDir(), "PRIVATE_STATE_PATH")
			state := root
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("HOME", "")
			switch kind {
			case "explicit-file":
				if e := os.WriteFile(root, nil, 0600); e != nil {
					t.Fatal(e)
				}
			default:
				dir := filepath.Join(root, "InterProcessKV")
				if e := os.MkdirAll(dir, 0700); e != nil {
					t.Fatal(e)
				}
				identity := filepath.Join(dir, "device_uuid")
				switch kind {
				case "directory-mode":
					os.Chmod(dir, 0755)
				case "identity-mode":
					os.WriteFile(identity, []byte("PRIVATE_IDENTITY"), 0644)
				case "identity-symlink":
					os.Symlink("PRIVATE_SYMLINK_TARGET", identity)
				case "identity-dir":
					os.Mkdir(identity, 0700)
				case "identity-invalid":
					os.WriteFile(identity, []byte("PRIVATE_IDENTITY\n"), 0600)
				case "identity-oversize":
					os.WriteFile(identity, bytes.Repeat([]byte("x"), 4097), 0600)
				case "identity-valid":
					os.WriteFile(identity, []byte("opaque-existing-identifier"), 0600)
				}
			}
			c, err := cloudhttp.NewFromEnvironment(cloudhttp.Config{StateDir: state})
			if c != nil {
				c.Close()
			}
			if kind == "identity-valid" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, cloud.ErrConfig) {
				t.Fatalf("want safe ErrConfig got %v", err)
			}
			var option *diagnostic.OptionError
			if !errors.As(err, &option) || option.Option != "--state-dir" || option.Rule == "" {
				t.Errorf("missing state option/rule: %v", err)
			}
			assertNoSensitiveErrorDetails(t, err, "PRIVATE_STATE_PATH", "PRIVATE_IDENTITY", "PRIVATE_PASSWORD", "PRIVATE_SYMLINK_TARGET")
			t.Logf("safe returned failure: %v", err)
		})
	}
}
