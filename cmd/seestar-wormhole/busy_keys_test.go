package main

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/state"
)

// A history or folder lock held elsewhere reads as phase open, reason busy
// in a command's failure record, as it does in the app's trail. The record
// keeps the original error text and the exit code is unchanged.
func TestLockedDestinationRecordsBusy(t *testing.T) {
	noReadOnlyGuard(t)
	for _, tc := range []struct {
		name, cmd, lock string
	}{
		{"sync history lock", "sync", "history"},
		{"sync folder lock", "sync", "folder"},
		{"delete folder lock", "delete", "folder"},
	} {
		for _, format := range []string{"text", "json"} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				dest, history := t.TempDir(), t.TempDir()
				var held error
				switch tc.lock {
				case "history":
					s, err := state.Open(history, dest, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
					held = state.ErrLocked
				case "folder":
					root, err := os.OpenRoot(dest)
					if err != nil {
						t.Fatal(err)
					}
					defer root.Close()
					lock, err := state.LockFolder(root)
					if err != nil {
						t.Fatal(err)
					}
					defer lock.Close()
					held = state.ErrFolderLocked
				}
				args := []string{tc.cmd, "--endpoint", "127.0.0.1:1", "--dest", dest, "--history-dir", history, "--log-format", format}
				if tc.cmd == "sync" {
					args = append(args, "--once")
				} else {
					args = append(args, "--force")
				}
				var out, logs sharedBuffer
				if code := runFileCommand(context.Background(), args, &out, &logs); code != 1 {
					t.Fatalf("exit %d, want 1; logs %s", code, logs.String())
				}
				all := records(t, format, logs.String())
				i := slices.IndexFunc(all, func(r map[string]any) bool { return r["msg"] == tc.cmd+" failed" })
				if i < 0 {
					t.Fatalf("no %q record in %s", tc.cmd+" failed", logs.String())
				}
				r := all[i]
				if r["source"] != map[string]string{"sync": "copy", "delete": "delete"}[tc.cmd] || r["op"] != "run" ||
					r["phase"] != "open" || r["reason"] != "busy" {
					t.Errorf("record %v, want op=run phase=open reason=busy", r)
				}
				text, _ := r["error"].(string)
				if !strings.Contains(text, held.Error()) {
					t.Errorf("error %q does not keep %q", text, held.Error())
				}
			})
		}
	}
}
