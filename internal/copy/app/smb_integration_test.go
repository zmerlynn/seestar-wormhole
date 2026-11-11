//go:build smbintegration

package app

// This test needs the Samba fixture in internal/copy/remote/testdata/samba,
// reachable at SEESTAR_COPY_SMB_ENDPOINT (host:port). scripts/test-smb.sh
// and CI start it.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/plan"
	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
)

// TestSMBCopyPass runs the controller, with no window, for one pass from
// the fixture through the real SMB client, then stops it.
func TestSMBCopyPass(t *testing.T) {
	endpoint := os.Getenv("SEESTAR_COPY_SMB_ENDPOINT")
	if endpoint == "" {
		t.Fatal("SEESTAR_COPY_SMB_ENDPOINT must name the Samba fixture (host:port)")
	}
	set := DefaultSettings()
	set.Address = endpoint
	set.Folder = filepath.Join(t.TempDir(), "Seestar")
	set.Includes = []string{"/MyWorks/M 31"}
	dial := func(ctx context.Context, tg Target) (remote.Session, error) {
		return remote.Dial(ctx, tg.Endpoint, nil)
	}
	c := newController(t.Context(), t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)), dial)
	t.Cleanup(c.quit)
	if err := c.start(set, Target{Endpoint: set.Address}); err != nil {
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

// TestSMBFreeUpSpace copies a folder of the fixture's scratch share through
// the real SMB client, previews Free up space, leaves two of its folders
// unchecked, copies one more file into a checked folder and confirms: only
// the previewed files in the checked folders are deleted.
func TestSMBFreeUpSpace(t *testing.T) {
	endpoint := os.Getenv("SEESTAR_COPY_SMB_ENDPOINT")
	if endpoint == "" {
		t.Fatal("SEESTAR_COPY_SMB_ENDPOINT must name the Samba fixture (host:port)")
	}
	ctx := t.Context()
	files := map[string]string{
		"app-free/A/a1.fit": "a1", "app-free/A/a2.fit": "a2", "app-free/B/b.fit": "b",
		"app-free/C/c.fit": "c", "app-free/D/d.fit": "d",
	}
	r, err := remote.DialScratch(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := remote.FillScratch(ctx, r, "app-free", files); err != nil {
		t.Fatal(err)
	}
	set := DefaultSettings()
	set.Address, set.Folder, set.OlderThanDays = endpoint, filepath.Join(t.TempDir(), "Seestar"), 0
	set.Includes = []string{"/app-free"}
	stateDir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dial := func(ctx context.Context, tg Target) (remote.Session, error) {
		return remote.DialScratch(ctx, tg.Endpoint)
	}

	// pass runs one copy pass, which must copy what summary begins with.
	pass := func(summary string) {
		t.Helper()
		c := newController(ctx, stateDir, log, dial)
		defer c.quit()
		if err := c.start(set, Target{Endpoint: set.Address}); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(time.Minute); ; time.Sleep(10 * time.Millisecond) {
			v := c.view(time.Now())
			if v.State == stUpToDate || v.State == stFailures || v.State == stUnreachable || v.State == stCantWrite {
				if v.State != stUpToDate || !strings.HasPrefix(v.Summary, summary) {
					t.Fatalf("after one pass: %+v", v)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("no pass ended: %+v", v)
			}
		}
	}
	pass("Copied 5 files (")

	f := newFreer(ctx, stateDir, log, dial)
	f.remover = func(ctx context.Context, tg Target) (remote.Remover, error) {
		return remote.DialScratch(ctx, tg.Endpoint)
	}
	t.Cleanup(f.quit)
	wait := func(states ...string) freeView {
		t.Helper()
		for deadline := time.Now().Add(time.Minute); ; time.Sleep(10 * time.Millisecond) {
			v := f.view()
			if slices.Contains(states, v.State) {
				return v
			}
			if time.Now().After(deadline) {
				t.Fatalf("state %q, want one of %q: %+v", v.State, states, v)
			}
		}
	}
	if err := f.find(set, Target{Endpoint: set.Address}); err != nil {
		t.Fatal(err)
	}
	v := wait(frPreview, frNothing, frCheckFail)
	var folders []string
	for _, g := range v.Groups {
		folders = append(folders, g.Folder)
	}
	if v.State != frPreview || !slices.Equal(folders, []string{"app-free/A", "app-free/B", "app-free/C", "app-free/D"}) {
		t.Fatalf("preview: %+v", v)
	}
	// A file copied after the preview, in a folder to be checked, is
	// eligible now but was not previewed: it stays.
	if err := remote.PutScratch(ctx, r, "app-free/A/new.fit", "new"); err != nil {
		t.Fatal(err)
	}
	pass("Copied 1 file (")
	if err := f.confirm(v.ID, []string{"app-free/A", "app-free/B"}, Target{Endpoint: set.Address}); err != nil {
		t.Fatal(err)
	}
	if v := wait(frDone, frStopped, frFailed); v.State != frDone || v.Title != "Deleted 3 files and removed 1 empty folder." {
		t.Errorf("done: %+v", v)
	}
	var left []string
	dirs, err := r.ReadDir(ctx, "app-free")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		in, err := r.ReadDir(ctx, d.Path)
		if err != nil {
			t.Fatal(err)
		}
		left = append(left, d.Path)
		for _, e := range in {
			left = append(left, e.Path)
		}
	}
	slices.Sort(left)
	if want := []string{"app-free/A", "app-free/A/new.fit", "app-free/C", "app-free/C/c.fit", "app-free/D", "app-free/D/d.fit"}; !slices.Equal(left, want) {
		t.Errorf("left on the share: %q, want %q", left, want)
	}
}

// TestSMBFreeUpSpaceUnchecked previews and deletes, through the real SMB
// client, files that were never copied, with Free up space checking
// nothing: the unchecked folder stays.
func TestSMBFreeUpSpaceUnchecked(t *testing.T) {
	endpoint := os.Getenv("SEESTAR_COPY_SMB_ENDPOINT")
	if endpoint == "" {
		t.Fatal("SEESTAR_COPY_SMB_ENDPOINT must name the Samba fixture (host:port)")
	}
	ctx := t.Context()
	r, err := remote.DialScratch(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := remote.FillScratch(ctx, r, "app-unchecked", map[string]string{
		"app-unchecked/A/a.fit": "a", "app-unchecked/B/b.fit": "b",
	}); err != nil {
		t.Fatal(err)
	}
	set := DefaultSettings()
	set.Address, set.Folder, set.OlderThanDays, set.IgnoreState = endpoint, filepath.Join(t.TempDir(), "Seestar"), 0, true
	set.FreeIncludes = []string{"/app-unchecked"}
	f := newFreer(ctx, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context, tg Target) (remote.Session, error) {
		return remote.DialScratch(ctx, tg.Endpoint)
	})
	f.remover = func(ctx context.Context, tg Target) (remote.Remover, error) {
		return remote.DialScratch(ctx, tg.Endpoint)
	}
	t.Cleanup(f.quit)
	wait := func(states ...string) freeView {
		t.Helper()
		for deadline := time.Now().Add(time.Minute); ; time.Sleep(10 * time.Millisecond) {
			v := f.view()
			if slices.Contains(states, v.State) {
				return v
			}
			if time.Now().After(deadline) {
				t.Fatalf("state %q, want one of %q: %+v", v.State, states, v)
			}
		}
	}
	if err := f.find(set, Target{Endpoint: set.Address}); err != nil {
		t.Fatal(err)
	}
	v := wait(frPreview, frNothing, frCheckFail)
	if v.State != frPreview || v.Title != "2 files (2 B) are selected to delete." || !v.Ignore {
		t.Fatalf("preview: %+v", v)
	}
	if err := f.confirm(v.ID, []string{"app-unchecked/A"}, Target{Endpoint: set.Address}); err != nil {
		t.Fatal(err)
	}
	if v := wait(frDone, frStopped, frFailed); v.State != frDone || v.Title != "Deleted 1 file and removed 1 empty folder." {
		t.Errorf("done: %+v", v)
	}
	dirs, err := r.ReadDir(ctx, "app-unchecked")
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, d := range dirs {
		left = append(left, d.Path)
	}
	if want := []string{"app-unchecked/B"}; !slices.Equal(left, want) {
		t.Errorf("left on the share: %q, want %q", left, want)
	}
	if _, err := os.Stat(set.Folder); !os.IsNotExist(err) {
		t.Errorf("the copy folder was touched: %v", err)
	}
}
