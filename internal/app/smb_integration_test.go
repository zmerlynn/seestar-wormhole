//go:build smbintegration

package app

// This test needs the Samba fixture in internal/remote/testdata/samba,
// reachable at SEESTAR_COPY_SMB_ENDPOINT (host:port). scripts/test-smb.sh
// and CI start it.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/remote"
)

// TestSMBCopyPass runs the controller, with no window, for one pass from
// the fixture through the real SMB client, then stops it.
func TestSMBCopyPass(t *testing.T) {
	endpoint := os.Getenv("SEESTAR_COPY_SMB_ENDPOINT")
	if endpoint == "" {
		t.Fatal("SEESTAR_COPY_SMB_ENDPOINT must name the Samba fixture (host:port)")
	}
	set := defaultSettings()
	set.Address = endpoint
	set.Folder = filepath.Join(t.TempDir(), "Seestar")
	set.Includes = []string{"/MyWorks/M 31"}
	dial := func(ctx context.Context, endpoint string) (remote.Session, error) { return remote.Dial(ctx, endpoint) }
	c := newController(t.Context(), t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)), dial)
	t.Cleanup(c.quit)
	if err := c.start(set); err != nil {
		t.Fatal(err)
	}
	var v copyView
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(10 * time.Millisecond) {
		v = c.view(time.Now())
		if v.State == stUpToDate || v.State == stFailures || v.State == stUnreachable || v.State == stCantWrite {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pass ended: %+v", v)
		}
	}
	if v.State != stUpToDate || v.Contact != "ok" || !strings.HasPrefix(v.Summary, "Copied 3 files (") {
		t.Fatalf("after one pass: %+v", v)
	}
	want := map[string]int64{
		"MyWorks/M 31/Light_M 31_10.0s_IRCUT_20261029-033119.fit": 16384,
		"MyWorks/M 31/Light_M 31_10.0s_IRCUT_20261029-033129.fit": 16385,
		"MyWorks/M 31/sub dir/deeper/x.fit":                       77,
	}
	for p, size := range want {
		fi, err := os.Stat(filepath.Join(set.Folder, filepath.FromSlash(p)))
		if err != nil || fi.Size() != size {
			t.Errorf("%s: %v, %v; want %d bytes", p, fi, err, size)
		}
	}
	c.stop()
	c.quit()
	if v := c.view(time.Now()); v.State != stOff || v.On {
		t.Errorf("after Stop: %+v", v)
	}
	err := filepath.WalkDir(set.Folder, func(p string, d os.DirEntry, err error) error {
		if err == nil && plan.IsTempName(d.Name()) {
			t.Errorf("temporary file left: %s", p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
