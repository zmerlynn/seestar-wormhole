package main

import (
	"bytes"
	"context"
	"github.com/go-json-experiment/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
)

type rawListingFs struct {
	fs.Fs
	name string
}

func (f rawListingFs) List(context.Context, string) (fs.DirEntries, error) {
	return fs.DirEntries{fs.NewDir(f.name, time.Now().Add(-time.Hour))}, nil
}

func TestPullRawListingExit(t *testing.T) {
	for _, name := range []string{"../escape", "NUL.fit", "elsewhere/child"} {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			base, err := local.NewFs(t.Context(), "fixture", t.TempDir(), configmap.Simple{})
			if err != nil {
				t.Fatal(err)
			}
			o, err := parse([]string{"pull", "--only=selected", "--output=json", dest}, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			var out, logs bytes.Buffer
			s := &pullSessionFixture{}
			err = runParsedPullWithSource(t.Context(), o, &out, &logs, keylog.Open,
				func(context.Context, proxy.Config) (commandSession, error) { return s, nil },
				func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
					return ctx, rawListingFs{base, name}, nil
				})
			if code := pullCode(err); code != 1 {
				t.Fatalf("exit=%d want 1; out=%s", code, &out)
			}
			var summary struct {
				Status, Reason string
				ExitCode       int `json:"exit_code"`
			}
			if err := json.Unmarshal(out.Bytes(), &summary, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatal(err)
			}
			if summary.Status != "error" || summary.Reason != "unsafe_path" || summary.ExitCode != 1 {
				t.Fatalf("summary=%+v", summary)
			}
			d, err := pull.OpenDestination(dest)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if len(d.State.Files) != 0 || len(d.State.Selections) != 0 {
				t.Fatal("refusal wrote receipts/checkpoint")
			}
			if _, err := os.Stat(filepath.Join(dest, ".seestar-wormhole", "state.json")); !os.IsNotExist(err) {
				t.Fatalf("refusal saved state: %v", err)
			}
		})
	}
}
