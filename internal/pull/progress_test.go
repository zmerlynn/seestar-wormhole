package pull

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
)

func TestProgressFileAndByteAccounting(t *testing.T) {
	for _, verify := range []bool{false, true} {
		t.Run(map[bool]string{false: "copy", true: "verify"}[verify], func(t *testing.T) {
			c, d, src, root, now := fixture(t)
			putFile(t, root, "a.fit", "1234567", now.Add(-time.Hour))
			putFile(t, root, "recent.fit", "deferred", now.Add(-time.Minute))
			ctx, p := NewProgress(t.Context())
			c.Progress, c.Verify = p, verify
			if _, err := d.Run(ctx, c, now, src); err != nil {
				t.Fatal(err)
			}
			wantBytes := int64(7)
			if verify {
				wantBytes = 21
			} // copy plus source/local download comparison
			if got := p.Snapshot(); got.FilesDone != 1 || got.FilesTotal != 1 || got.BytesDone != wantBytes || got.BytesTotal != wantBytes {
				t.Fatalf("completed=%+v want files=1/1 bytes=%d/%d", got, wantBytes, wantBytes)
			}
			// A fresh command does not retain old accounting. Receipt skips and
			// deferred files cause no content work and are not in the denominator.
			ctx, c.Progress = NewProgress(t.Context())
			if _, err := d.Run(ctx, c, now, src); err != nil {
				t.Fatal(err)
			}
			if got := c.Progress.Snapshot(); got != (ProgressSnapshot{}) {
				t.Fatalf("repeat=%+v", got)
			}
			ctx, c.Progress = NewProgress(t.Context())
			c.Recheck = true
			if _, err := d.Run(ctx, c, now, src); err != nil {
				t.Fatal(err)
			}
			if got := c.Progress.Snapshot(); got.FilesDone != 1 || got.FilesTotal != 1 || got.BytesDone != 14 || got.BytesTotal != 14 {
				t.Fatalf("recheck=%+v", got)
			}
		})
	}
}

type progressFailFs struct{ fs.Fs }

func (f progressFailFs) NewObject(context.Context, string) (fs.Object, error) {
	return nil, io.ErrUnexpectedEOF
}

func TestProgressFailedFileIncomplete(t *testing.T) {
	c, d, src, root, now := fixture(t)
	putFile(t, root, "a.fit", "1234567", now.Add(-time.Hour))
	ctx, p := NewProgress(t.Context())
	c.Progress = p
	_, err := d.Run(ctx, c, now, progressFailFs{src})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("failure=%v", err)
	}
	if got := p.Snapshot(); got.FilesDone != 0 || got.FilesTotal != 1 {
		t.Fatalf("failed=%+v", got)
	}
}

func TestProgressLiveRcloneRate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, p := NewProgress(t.Context())
		tr := accounting.Stats(ctx).NewTransferRemoteSize("PRIVATE_FILE", 8, nil, nil)
		r := tr.Account(ctx, io.NopCloser(strings.NewReader("12345678")))
		if _, err := io.ReadFull(r, make([]byte, 4)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := p.Snapshot(); got.BytesDone != 4 || got.BytesTotal != 8 || got.Rate != 4 {
			t.Fatalf("live=%+v want bytes=4/8 rate=4", got)
		}
		tr.Done(ctx, nil)
	})
}
