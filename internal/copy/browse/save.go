package browse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/readonly"
)

// Hold runs a save's rename, the one step that puts the file at its
// destination, and may refuse it with an error instead: the save then
// removes its temporary file and returns that error. A nil Hold renames.
type Hold func(rename func() error) error

// Save copies the device's file at share path p to dst, through lease's
// session, which the caller releases. It writes a temporary file beside dst, checks that it got
// every byte the device listed and that the file closed cleanly, then
// renames it over dst through hold. On any error it removes the temporary
// file, so an existing dst is left as it was. ctx ends the copy between
// reads.
func Save(ctx context.Context, lease *Lease, p, dst string, hold Hold) error {
	readonly.Check("browse.Save")
	dir, base := filepath.Dir(dst), filepath.Base(dst)
	return save(ctx, lease, p, hold, func() (*os.File, error) {
		return os.CreateTemp(dir, "."+base+".*.tmp")
	}, func(tmp string) error {
		return os.Rename(tmp, dst)
	}, func(tmp string) { os.Remove(tmp) })
}

// SaveIn is [Save] to rel, a path relative to root, with the temporary
// file, the rename and the cleanup all through root: the caller holds a
// lock that guards the folder root is open on, such as its folder lock.
func SaveIn(ctx context.Context, lease *Lease, p string, root *os.Root, rel string, hold Hold) error {
	readonly.Check("browse.SaveIn")
	dir, base := filepath.Dir(rel), filepath.Base(rel)
	var tmpRel string
	return save(ctx, lease, p, hold, func() (*os.File, error) {
		for range 10000 {
			tmpRel = filepath.Join(dir, "."+base+"."+strconv.FormatUint(uint64(rand.Uint32()), 10)+".tmp")
			f, err := root.OpenFile(tmpRel, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
			if !errors.Is(err, fs.ErrExist) {
				return f, err
			}
		}
		return nil, &fs.PathError{Op: "createtemp", Path: filepath.Join(dir, "."+base+".*.tmp"), Err: fs.ErrExist}
	}, func(string) error {
		return root.Rename(tmpRel, rel)
	}, func(string) { root.Remove(tmpRel) })
}

// save copies the device's file at p into a temporary file from create,
// then calls commit with its name, through hold; on any error, hold's
// refusal too, it calls discard instead.
func save(ctx context.Context, lease *Lease, p string, hold Hold, create func() (*os.File, error), commit func(tmp string) error, discard func(tmp string)) (err error) {
	readonly.Check("browse.save")
	f, err := lease.Session().Open(lease.Context(), p)
	if err != nil {
		lease.Fail(err)
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		lease.Fail(err)
		return err
	}
	if info.IsDir() {
		return errors.New("it is a folder on the device")
	}
	out, err := create()
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer func() {
		if err != nil {
			out.Close() // an error here means it was closed already
			discard(tmp)
		}
	}()
	size := info.Size()
	n, err := io.CopyBuffer(out, &saveReader{ctx: ctx, f: f, fail: lease.Fail}, make([]byte, max(min(size, maxBuffer), 512)))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("the device sent %d of its %d bytes", n, size)
	}
	if err := out.Close(); err != nil {
		return err
	}
	if hold == nil {
		return commit(tmp)
	}
	return hold(func() error { return commit(tmp) })
}

// saveReader reads a device file until ctx is done, and reports each
// device error to fail.
type saveReader struct {
	ctx  context.Context
	f    io.Reader
	fail func(error)
}

func (r *saveReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.f.Read(p)
	if err != nil && err != io.EOF {
		r.fail(err)
	}
	return n, err
}
