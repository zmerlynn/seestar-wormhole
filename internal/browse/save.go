package browse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zmerlynn/seestar-copy/internal/readonly"
)

// Save copies the device's file at share path p to dst, through a session
// from sessions. It writes a temporary file beside dst, checks that it got
// every byte the device listed and that the file closed cleanly, then
// renames it over dst. On any error it removes the temporary file, so an
// existing dst is left as it was. ctx ends the copy between reads.
func Save(ctx context.Context, sessions *Sessions, p, dst string) (err error) {
	readonly.Check("browse.Save")
	lease, err := sessions.Get(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
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
	out, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer func() {
		if err != nil {
			out.Close() // an error here means it was closed already
			os.Remove(tmp)
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
	return os.Rename(tmp, dst)
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
