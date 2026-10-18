package pull

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/hash"
	"github.com/rclone/rclone/fs/object"
	"github.com/rclone/rclone/fs/operations"
)

// receiptSnapshot feeds already verified hashes to rclone's standard HashLister.
// It never hashes unrelated local files or rewrites prior receipts from current,
// potentially modified bytes. This is an immutable inventory, not a file walker.
type receiptSnapshot struct {
	fs.Fs
	features fs.Features
}

func (s *receiptSnapshot) Features() *fs.Features { return &s.features }

type receiptObject struct{ fs.ObjectInfo }

func (receiptObject) Open(context.Context, ...fs.OpenOption) (io.ReadCloser, error) {
	return nil, fs.ErrorPermissionDenied
}
func (receiptObject) SetModTime(context.Context, time.Time) error { return fs.ErrorPermissionDenied }
func (receiptObject) Update(context.Context, io.Reader, fs.ObjectInfo, ...fs.OpenOption) error {
	return fs.ErrorPermissionDenied
}
func (receiptObject) Remove(context.Context) error { return fs.ErrorPermissionDenied }

type checkedWriter struct {
	io.Writer
	err error
}

// Rclone's SyncFprintf serializes all writes but discards their errors. Preserve
// the first failure so a full disk cannot yield a successful checkpoint commit.
func (w *checkedWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if w.err == nil {
		w.err = err
	}
	return n, err
}

func (d *Destination) manifest(ctx context.Context) error {
	base, err := local.NewFs(ctx, "verified-receipts", d.path, configmap.Simple{})
	if err != nil {
		return err
	}
	snapshot := &receiptSnapshot{Fs: base, features: *base.Features()}
	snapshot.features.ListR = func(ctx context.Context, _ string, callback fs.ListRCallback) error {
		paths := make([]string, 0, len(d.State.Files))
		for p := range d.State.Files {
			paths = append(paths, p)
		}
		slices.Sort(paths)
		entries := make(fs.DirEntries, 0, 1024)
		for _, p := range paths {
			if err := ctx.Err(); err != nil {
				return err
			}
			r := d.State.Files[p]
			entries = append(entries, receiptObject{object.NewStaticObjectInfo(p, r.Mtime, r.Size, true, map[hash.Type]string{hash.SHA256: r.SHA256}, snapshot)})
			if len(entries) == 1024 {
				if err := callback(entries); err != nil {
					return err
				}
				entries = entries[:0]
			}
		}
		if len(entries) > 0 {
			return callback(entries)
		}
		return nil
	}
	token, err := randomName()
	if err != nil {
		return err
	}
	temporary := filepath.Join(d.path, ".seestar-sums-"+token)
	f, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	writer := &checkedWriter{Writer: f}
	ctx, ci := fs.AddConfig(ctx)
	ci.Checkers = 1 // deterministic sorted standard manifest
	// Portable preflight rejects backslash/control characters, so HashLister's
	// two-space GNU sha256sum form needs no escaping marker for these paths.
	err = operations.HashLister(ctx, hash.SHA256, false, false, snapshot, writer)
	if err == nil {
		err = writer.err
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = checkLocalPath(filepath.Join(d.path, "SHA256SUMS")); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(temporary, filepath.Join(d.path, "SHA256SUMS")); err != nil {
		return err
	}
	return syncDirectory(d.path)
}
