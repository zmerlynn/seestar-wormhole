package copier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/plan"
	"github.com/zmerlynn/seestar-copy/internal/readonly"
	"github.com/zmerlynn/seestar-copy/internal/remote"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

// syncFile makes a file's or a folder's contents durable. Tests replace it
// to check what is synced, and when.
var syncFile = (*os.File).Sync

// errTooLong reports a file that holds more than its listed size.
var errTooLong = errors.New("the file is longer than its listed size")

// tempName returns the temporary file of share path p, relative to the
// root: [plan.TempName] in the folder of p.
func tempName(p string) string {
	return filepath.Join(filepath.FromSlash(path.Dir(p)), plan.TempName(p))
}

// download copies d from the device into place and records it as how.
func (c *Copier) download(ctx context.Context, sess remote.Session, d plan.Decision, how state.How) (rec state.Record, err error) {
	readonly.Check("copier.download")
	if err := c.root.MkdirAll(filepath.FromSlash(path.Dir(d.Path)), 0o777); err != nil {
		return rec, fmt.Errorf("create the folder: %w", err)
	}
	tmp := tempName(d.Path)
	// A leftover of a failed cleanup is removed first, so the exclusive
	// create never opens an existing file or link.
	if err := c.root.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return rec, fmt.Errorf("remove an old temporary file: %w", err)
	}
	f, err := c.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return rec, fmt.Errorf("create the temporary file: %w", err)
	}
	defer func() {
		if err == nil {
			return
		}
		f.Close() // an error here means it was closed already
		// After the rename the temporary file is gone, and there is
		// nothing to remove.
		if rerr := c.root.Remove(tmp); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			err = fmt.Errorf("%w; removing the temporary file failed too: %w", err, rerr)
		}
	}()
	var w io.Writer = f
	if c.Copying != nil {
		pw := &progressWriter{w: f, path: d.Path, size: d.Size, last: time.Now(), report: c.Copying}
		w = pw
		defer func() { c.Copying(d.Path, pw.done, d.Size) }()
	}
	sum, err := fetch(ctx, sess, d, w)
	if err != nil {
		return rec, err
	}
	if err := syncFile(f); err != nil {
		return rec, fmt.Errorf("fsync the temporary file: %w", err)
	}
	if err := f.Close(); err != nil {
		return rec, fmt.Errorf("close the temporary file: %w", err)
	}
	// Set after closing: Windows may update the mtime when a written
	// handle closes. A zero atime is left as it is.
	if err := c.root.Chtimes(tmp, time.Time{}, d.ModTime); err != nil {
		return rec, fmt.Errorf("set the mtime: %w", err)
	}
	if err := c.root.Rename(tmp, filepath.FromSlash(d.Path)); err != nil {
		return rec, fmt.Errorf("rename into place: %w", err)
	}
	// File.Sync cannot fsync a folder on Windows, so there a crash may lose
	// the rename of a recorded file: a documented gap.
	if runtime.GOOS != "windows" {
		if err := c.syncFolder(path.Dir(d.Path)); err != nil {
			return rec, fmt.Errorf("fsync the folder: %w", err)
		}
	}
	return c.record(d, sum, how)
}

// fetch reads d from the device into w and returns the lowercase hex
// SHA-256 of what it read, which must be exactly d.Size bytes.
func fetch(ctx context.Context, sess remote.Session, d plan.Decision, w io.Writer) (string, error) {
	rf, err := sess.Open(ctx, d.Path)
	if err != nil {
		return "", fmt.Errorf("open on the device: %w", err)
	}
	// Closing a file opened read-only cannot lose data, so its error
	// changes nothing.
	defer rf.Close()
	sum, err := hashExactly(ctx, w, rf, d.Size)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	return sum, nil
}

// adopt hashes the local copy of d and records it as adopted. The file must
// be a regular file with the listed size and an mtime within
// plan.SameMTime of the listed one both before and after it is hashed, and
// must still be the file at the path afterward. It is only read. The record
// keeps the device's mtime.
func (c *Copier) adopt(ctx context.Context, d plan.Decision) (state.Record, error) {
	readonly.Check("copier.adopt")
	name := filepath.FromSlash(d.Path)
	f, err := c.root.Open(name)
	if err != nil {
		return state.Record{}, fmt.Errorf("open the local file: %w", err)
	}
	defer f.Close()
	if _, err := checkLocal(f, d); err != nil {
		return state.Record{}, err
	}
	sum, err := hashExactly(ctx, io.Discard, f, d.Size)
	if err != nil {
		return state.Record{}, fmt.Errorf("hash the local file: %w", err)
	}
	// Another process may have written the file, or put another file at
	// the path, while it was hashed.
	fi, err := checkLocal(f, d)
	if err != nil {
		return state.Record{}, fmt.Errorf("after hashing: %w", err)
	}
	at, err := c.root.Lstat(name)
	if err != nil {
		return state.Record{}, fmt.Errorf("stat the local path after hashing: %w", err)
	}
	// The open file is regular, so a link or anything else at the path is
	// not the same file.
	if !os.SameFile(fi, at) {
		return state.Record{}, errors.New("the local path no longer leads to the file that was hashed")
	}
	return c.record(d, sum, state.Adopted)
}

// checkLocal returns the stat of f, the open local file of d, if it is a
// regular file with d's size and an mtime within plan.SameMTime of d's.
func checkLocal(f *os.File, d plan.Decision) (fs.FileInfo, error) {
	fi, err := f.Stat()
	switch {
	case err != nil:
		return nil, fmt.Errorf("stat the local file: %w", err)
	case !fi.Mode().IsRegular():
		return nil, errors.New("the local file is no longer a regular file")
	case fi.Size() != d.Size || !plan.SameMTime(fi.ModTime(), d.ModTime):
		return nil, fmt.Errorf("the local file changed: it has size %d and mtime %s, not %d and %s",
			fi.Size(), fi.ModTime().UTC().Format(time.RFC3339Nano), d.Size, d.ModTime.UTC().Format(time.RFC3339Nano))
	}
	return fi, nil
}

// record appends the state record of d, which is complete under its final
// name.
func (c *Copier) record(d plan.Decision, sum string, how state.How) (state.Record, error) {
	readonly.Check("copier.record")
	rec := state.Record{Path: d.Path, Size: d.Size, MTime: d.ModTime.UTC(), SHA256: sum, At: c.now().UTC(), How: how}
	if err := c.state.Append(rec); err != nil {
		return state.Record{}, fmt.Errorf("record in the state: %w", err)
	}
	return rec, nil
}

// syncFolder opens share folder dir, fsyncs it and closes it.
func (c *Copier) syncFolder(dir string) error {
	readonly.Check("copier.syncFolder")
	f, err := c.root.Open(filepath.FromSlash(dir))
	if err != nil {
		return err
	}
	err = syncFile(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// hashExactly copies src to w and returns the lowercase hex SHA-256 of what
// it copied, which must be exactly size bytes. It stops once ctx is done.
func hashExactly(ctx context.Context, w io.Writer, src io.WriterTo, size int64) (string, error) {
	h := sha256.New()
	lw := &limitWriter{ctx: ctx, w: io.MultiWriter(w, h), left: size}
	if _, err := src.WriteTo(lw); err != nil {
		return "", fmt.Errorf("stopped after %d of %d bytes: %w", size-lw.left, size, err)
	}
	if lw.left != 0 {
		return "", fmt.Errorf("short read: got %d of %d bytes", size-lw.left, size)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// limitWriter passes at most left bytes to w, and fails once ctx is done.
type limitWriter struct {
	ctx  context.Context
	w    io.Writer
	left int64
}

func (lw *limitWriter) Write(p []byte) (int, error) {
	if err := lw.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > lw.left {
		return 0, errTooLong
	}
	n, err := lw.w.Write(p)
	lw.left -= int64(n)
	return n, err
}

// progressWriter passes writes to w and reports the bytes written so far
// to report at most once a second. It sits below the hash's writer, so the
// device file's WriteTo still drives the copy.
type progressWriter struct {
	w      io.Writer
	path   string
	size   int64
	done   int64
	last   time.Time // of the last report, or of the start
	report func(path string, done, size int64)
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	pw.done += int64(n)
	if now := time.Now(); now.Sub(pw.last) >= time.Second {
		pw.last = now
		pw.report(pw.path, pw.done, pw.size)
	}
	return n, err
}
