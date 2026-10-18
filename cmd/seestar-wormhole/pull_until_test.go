package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPullUntilFlag(t *testing.T) {
	for _, value := range []string{"2h", "2026-10-13T06:15:00.125-05:00"} {
		if _, err := parse([]string{"pull", "--version", "--until=" + value}, io.Discard); err != nil {
			t.Fatalf("until %q: %v", value, err)
		}
	}
	for _, value := range []string{"", "auto", "all", "-1h", "0", "1d", "2026-10-13", "2026-10-13T06:15:00", "PRIVATE_VALUE"} {
		if _, err := parse([]string{"pull", "--version", "--until=" + value}, io.Discard); err == nil {
			t.Fatalf("accepted invalid until %q", value)
		}
	}
}

func TestPullViaUntilSMB(t *testing.T) {
	endpoint := os.Getenv("SEESTAR_TEST_SAMBA_ADDR")
	if endpoint == "" {
		t.Skip("container Samba fixture required")
	}
	now := time.Now().UTC().Truncate(time.Second)
	until := now.Add(-2 * time.Hour)
	for name, mtime := range map[string]time.Time{"zeros.fit": until, "nested/small.fit": now.Add(-time.Hour)} {
		filename := filepath.Join("/tmp/smb-e2e/share/MyWorks", name)
		info, err := os.Stat(filename)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chtimes(filename, info.ModTime(), info.ModTime()) })
		if err = os.Chtimes(filename, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	dest := t.TempDir()
	value := until.Format(time.RFC3339Nano)
	code, out, _ := viaRun(t, t.Context(), dest, endpoint, "--until="+value)
	if code != 0 || !strings.Contains(out, "copied=1 verified=1 deferred=1 bytes=131072") || !strings.Contains(out, "upper="+value) {
		t.Fatalf("upper inclusive: %d %s", code, out)
	}
	d, err := pull.OpenDestination(dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range d.State.Selections {
		if !selection.Checkpoint.Equal(until) {
			t.Fatalf("checkpoint past until: %s", selection.Checkpoint)
		}
	}
	d.Close()
	code, out, _ = viaRun(t, t.Context(), dest, endpoint, "--until=2h", "--since=24h", "--recheck")
	if code != 0 || !strings.Contains(out, "verified=1 deferred=1") {
		t.Fatalf("duration until: %d %s", code, out)
	}
	code, out, _ = viaRun(t, t.Context(), dest, endpoint, "--until=90m", "--since=3h", "--recheck")
	if code != 0 || !strings.Contains(out, "verified=1 deferred=1") {
		t.Fatalf("duration window: %d %s", code, out)
	}
	code, out, _ = viaRun(t, t.Context(), dest, endpoint, "--until="+value, "--settle=3h")
	if code != 0 || !strings.Contains(out, "status=deferred") || !strings.Contains(out, "deferred=2") {
		t.Fatalf("settle cap: %d %s", code, out)
	}
}

func FuzzPullBoundsAndVia(f *testing.F) {
	for _, value := range []string{"", "2h", "2026-10-13T06:15:00.125-05:00", "[::1]:4445", "LOCALHOST:04445", "PRIVATE_MARKER:\ninvalid", "\x00"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 4096 {
			t.Skip()
		}
		for _, name := range []string{"since", "until", "via"} {
			var out, logs bytes.Buffer
			err := runWithSession(t.Context(), []string{"pull", "--version", "--" + name + "=" + value}, &out, &logs, keylog.Open, func(context.Context, proxy.Config) (commandSession, error) {
				t.Fatal("configuration contacted device")
				return nil, nil
			})
			if strings.Contains(logs.String()+out.String(), "PRIVATE_MARKER") {
				t.Fatal("configuration leaked input")
			}
			for e := err; e != nil; e = errors.Unwrap(e) {
				if strings.Contains(e.Error(), "PRIVATE_MARKER") {
					t.Fatal("returned error leaked input")
				}
			}
		}
	})
}
