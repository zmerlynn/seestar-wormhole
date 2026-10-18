package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

func runDirectoryCommand(t *testing.T, root, dest string, extra ...string) (int, string, int, int) {
	t.Helper()
	var out bytes.Buffer
	sessions, opens := 0, 0
	args := append([]string{"pull", "--settle=0", "--output=json"}, extra...)
	args = append(args, dest)
	err := runPullWithSource(t.Context(), args, &out, io.Discard, keylog.Open,
		func(context.Context, proxy.Config) (commandSession, error) {
			sessions++
			return &pullSessionFixture{}, nil
		},
		func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
			opens++
			sourceRoot := root
			if !strings.Contains(strings.Join(extra, " "), "--dir=") {
				sourceRoot = filepath.Join(root, "MyWorks")
			}
			src, err := local.NewFs(ctx, "directory-source", sourceRoot, configmap.Simple{})
			return ctx, src, err
		})
	return pullCode(err), out.String(), sessions, opens
}

func TestPullMissingDirectoryLeavesStateUnchanged(t *testing.T) {
	for _, bound := range []bool{false, true} {
		for _, rebind := range []bool{false, true} {
			name := fmt.Sprintf("bound=%t/rebind=%t", bound, rebind)
			t.Run(name, func(t *testing.T) {
				root, dest := t.TempDir(), t.TempDir()
				filename := filepath.Join(root, "MyWorks", "a.fit")
				if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filename, []byte("data"), 0600); err != nil {
					t.Fatal(err)
				}
				old := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
				if err := os.Chtimes(filename, old, old); err != nil {
					t.Fatal(err)
				}
				statePath := filepath.Join(dest, ".seestar-wormhole", "state.json")
				var before []byte
				if bound {
					if code, out, _, _ := runDirectoryCommand(t, root, dest, "--dir=MyWorks"); code != 0 {
						t.Fatalf("setup: code=%d out=%s", code, out)
					}
					var err error
					before, err = os.ReadFile(statePath)
					if err != nil {
						t.Fatal(err)
					}
				}
				extra := []string{"--dir=MyWorks", "--dir=missing"}
				if rebind {
					extra = append(extra, "--rebind")
				}
				code, out, _, _ := runDirectoryCommand(t, root, dest, extra...)
				if code != 1 || !strings.Contains(out, `"reason":"missing_directory"`) {
					t.Fatalf("code=%d out=%s", code, out)
				}
				after, err := os.ReadFile(statePath)
				if bound {
					if err != nil || !bytes.Equal(before, after) {
						t.Fatalf("missing directory changed state: err=%v\nbefore=%s\nafter=%s", err, before, after)
					}
				} else {
					if !os.IsNotExist(err) {
						t.Fatalf("missing directory created state: err=%v state=%s", err, after)
					}
					if _, err := os.Stat(filepath.Join(dest, "MyWorks")); !os.IsNotExist(err) {
						t.Fatalf("earlier directory transferred before inventory completed: %v", err)
					}
					if code, out, _, _ := runDirectoryCommand(t, root, dest); code != 0 || !strings.Contains(out, `"copied":1`) {
						t.Fatalf("default retry: code=%d out=%s", code, out)
					}
				}
			})
		}
	}
}
