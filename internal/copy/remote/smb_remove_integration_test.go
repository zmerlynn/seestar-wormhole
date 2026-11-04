//go:build smbintegration

package remote

// These tests remove files on the Samba fixture's guest-writable [scratch]
// share. Each builds its own folder there with go-smb2 directly.

import (
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/hirochachacha/go-smb2/v2"
)

// scratch returns a Remover on the scratch share and the raw go-smb2 share
// it uses, with an empty folder named dir to work in.
func scratch(t *testing.T, dir string) (*SMBRemover, *smb2.Share) {
	t.Helper()
	ctx := t.Context()
	s, err := dialShare(ctx, endpoint(t), newDialer(), "scratch")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	share := s.share
	// A folder left read-only by an earlier run would refuse RemoveAll.
	_ = share.Chmod(ctx, dir, 0o777)
	if err := share.RemoveAll(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if err := share.Mkdir(ctx, dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return &SMBRemover{s}, share
}

func writeScratch(t *testing.T, share *smb2.Share, name, data string) Entry {
	t.Helper()
	if err := share.WriteFile(t.Context(), name, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := share.Lstat(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	return Entry{Path: name, Size: fi.Size(), ModTime: fi.ModTime(), Kind: Regular}
}

func exists(t *testing.T, share *smb2.Share, name string) bool {
	t.Helper()
	_, err := share.Lstat(t.Context(), name)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

func TestSMBRemoveFile(t *testing.T) {
	ctx := t.Context()
	r, share := scratch(t, "file")
	e := writeScratch(t, share, "file/a.fit", "a")
	writeScratch(t, share, "file/b.fit", "b")
	if err := r.RemoveFile(ctx, e); err != nil {
		t.Fatalf("RemoveFile: %v", err)
	}
	if exists(t, share, "file/a.fit") {
		t.Error("file/a.fit is still there")
	}
	if !exists(t, share, "file/b.fit") {
		t.Error("sibling file/b.fit is gone")
	}
	if err := r.RemoveFile(ctx, e); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("RemoveFile of a missing file = %v, want fs.ErrNotExist", err)
	}
}

func TestSMBRemoveChangedFile(t *testing.T) {
	ctx := t.Context()
	r, share := scratch(t, "changed")
	e := writeScratch(t, share, "changed/a.fit", "a")
	later := e.ModTime.Add(time.Minute)
	if err := share.Chtimes(ctx, e.Path, later, later); err != nil {
		t.Fatal(err)
	}
	if err := r.RemoveFile(ctx, e); !errors.Is(err, ErrChanged) {
		t.Errorf("RemoveFile of a changed file = %v, want ErrChanged", err)
	}
	if !exists(t, share, e.Path) {
		t.Error("the changed file is gone")
	}
}

// TestSMBRemoveReadOnlyFile checks go-smb2's retry: a file with the
// read-only attribute is refused, its attribute cleared and the remove
// tried again.
func TestSMBRemoveReadOnlyFile(t *testing.T) {
	ctx := t.Context()
	r, share := scratch(t, "readonly")
	e := writeScratch(t, share, "readonly/a.fit", "a")
	if err := share.Chmod(ctx, e.Path, 0o444); err != nil {
		t.Fatal(err)
	}
	fi, err := share.Lstat(ctx, e.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o200 != 0 {
		t.Fatal("the fixture did not set the read-only attribute")
	}
	e.ModTime = fi.ModTime()
	if err := r.RemoveFile(ctx, e); err != nil {
		t.Fatalf("RemoveFile of a read-only file: %v", err)
	}
	if exists(t, share, e.Path) {
		t.Error("the read-only file is still there")
	}
}

func TestSMBRemoveDir(t *testing.T) {
	ctx := t.Context()
	r, share := scratch(t, "dir")
	if err := share.Mkdir(ctx, "dir/empty", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := share.Mkdir(ctx, "dir/full", 0o755); err != nil {
		t.Fatal(err)
	}
	writeScratch(t, share, "dir/full/a.fit", "a")
	if err := r.RemoveDir(ctx, "dir/full"); !errors.Is(err, ErrNotEmpty) {
		t.Errorf("RemoveDir of a folder that is not empty = %v, want ErrNotEmpty", err)
	}
	if !exists(t, share, "dir/full/a.fit") {
		t.Error("dir/full/a.fit is gone")
	}
	if err := r.RemoveDir(ctx, "dir/full/a.fit"); !errors.Is(err, ErrChanged) {
		t.Errorf("RemoveDir of a file = %v, want ErrChanged", err)
	}
	if err := r.RemoveDir(ctx, "dir/empty"); err != nil {
		t.Errorf("RemoveDir of an empty folder: %v", err)
	}
	if exists(t, share, "dir/empty") {
		t.Error("dir/empty is still there")
	}
	if err := r.RemoveDir(ctx, "dir/empty"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("RemoveDir of a missing folder = %v, want fs.ErrNotExist", err)
	}
}

// TestSMBRemoveReadOnlyDirLeavesAttributeCleared checks the documented
// side effect: when the retry after clearing the read-only attribute still
// fails, the attribute stays cleared.
func TestSMBRemoveReadOnlyDirLeavesAttributeCleared(t *testing.T) {
	ctx := t.Context()
	r, share := scratch(t, "rodir")
	if err := share.Mkdir(ctx, "rodir/full", 0o755); err != nil {
		t.Fatal(err)
	}
	writeScratch(t, share, "rodir/full/a.fit", "a")
	if err := share.Chmod(ctx, "rodir/full", 0o555); err != nil {
		t.Fatal(err)
	}
	if err := r.RemoveDir(ctx, "rodir/full"); !errors.Is(err, ErrNotEmpty) {
		t.Errorf("RemoveDir = %v, want ErrNotEmpty", err)
	}
	fi, err := share.Lstat(ctx, "rodir/full")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o200 == 0 {
		t.Error("the read-only attribute is still set; the retry was not reached")
	}
	if !exists(t, share, "rodir/full/a.fit") {
		t.Error("rodir/full/a.fit is gone")
	}
}

// TestSMBRemoveOnReadOnlyShare checks that the telescope-like share
// refuses a remove with a permission error and keeps the file.
func TestSMBRemoveOnReadOnlyShare(t *testing.T) {
	ctx := t.Context()
	r, err := DialRemover(ctx, endpoint(t))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	const path = "MyWorks/case/a.fit"
	fi, err := r.share.Lstat(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	e := Entry{Path: path, Size: fi.Size(), ModTime: fi.ModTime(), Kind: Regular}
	if err := r.RemoveFile(ctx, e); !errors.Is(err, fs.ErrPermission) {
		t.Errorf("RemoveFile on the read-only share = %v, want fs.ErrPermission", err)
	}
	if _, err := r.share.Lstat(ctx, e.Path); err != nil {
		t.Errorf("the file is gone: %v", err)
	}
}
