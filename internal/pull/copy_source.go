package pull

import (
	"context"
	"errors"
	"io"
	"sync/atomic"

	"github.com/rclone/rclone/fs"
)

// copySource counts actual source bytes through rclone's copy reader. A retry
// starts a new attempt; operations.Copy waits for its reader to close before
// retrying. Atomic counting also covers rclone's buffered reader goroutine.
type copySource struct {
	fs.Object
	bytes atomic.Int64
	eof   atomic.Bool
}

func (o *copySource) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) {
	r, err := o.Object.Open(ctx, options...)
	if err != nil {
		return nil, err
	}
	// A whole-copy retry starts at zero; a reader reopen resumes at a
	// nonzero range and must retain the bytes already read in this attempt.
	var offset int64
	for _, option := range options {
		switch option := option.(type) {
		case *fs.RangeOption:
			offset, _ = option.Decode(o.Size())
		case *fs.SeekOption:
			offset = option.Offset
		}
	}
	// Metadata has no error return; its guard cancels with the classified cause.
	// A failed reopen already owns a new reader which rclone will not close when
	// Open returns an error. Close it here after the guard has aborted transports.
	if err := context.Cause(ctx); err != nil {
		return nil, errors.Join(err, r.Close())
	}
	if offset == 0 {
		o.bytes.Store(0)
		o.eof.Store(false)
	}
	return copyReader{ReadCloser: r, bytes: &o.bytes, eof: &o.eof}, nil
}

type copyReader struct {
	io.ReadCloser
	bytes *atomic.Int64
	eof   *atomic.Bool
}

func (r copyReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytes.Add(int64(n))
	if err == io.EOF {
		r.eof.Store(true)
	}
	return n, err
}
