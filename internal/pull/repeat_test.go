package pull

import (
	"context"
	"github.com/rclone/rclone/fs"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Count source file opens through the standard backend, including objects
// returned by listing and by restat. Metadata listing remains uncounted.
type countedReadsFs struct {
	fs.Fs
	reads *atomic.Int64
	bytes *atomic.Int64
}

func (f countedReadsFs) wrap(o fs.Object) fs.Object {
	return injectedObject{o, func(ctx context.Context, opts ...fs.OpenOption) (io.ReadCloser, error) {
		f.reads.Add(1)
		reader, err := o.Open(ctx, opts...)
		if err != nil || f.bytes == nil {
			return reader, err
		}
		return countedReadCloser{reader, f.bytes}, nil
	}}
}
func (f countedReadsFs) List(ctx context.Context, dir string) (fs.DirEntries, error) {
	entries, err := f.Fs.List(ctx, dir)
	for i, e := range entries {
		if o, ok := e.(fs.Object); ok {
			entries[i] = f.wrap(o)
		}
	}
	return entries, err
}
func (f countedReadsFs) NewObject(ctx context.Context, name string) (fs.Object, error) {
	o, err := f.Fs.NewObject(ctx, name)
	if err != nil {
		return nil, err
	}
	return f.wrap(o), nil
}
func TestRepeatVerifiedListingOnly(t *testing.T) {
	c, d, source, root, now := fixture(t)
	c.Overlap = 2 * time.Hour
	putFile(t, root, "Dark/a.fit", "literal verified fixture", now.Add(-time.Hour))
	var reads atomic.Int64
	source = countedReadsFs{Fs: source, reads: &reads}
	first := runBatch(t, d, c, source, now)
	if first.Result.Copied != 1 || reads.Load() != 1 {
		t.Fatalf("first copied=%d reads=%d", first.Result.Copied, reads.Load())
	}
	// Restart state and backend metadata, as separate CLI invocations do.
	d.Close()
	d, err := OpenDestination(c.Destination)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, since := range []string{"auto", "all", "2026-10-13T10:00:00Z", "2h"} {
		c.Since = since
		reads.Store(0)
		b := runBatch(t, d, c, source, now.Add(time.Minute))
		if reads.Load() != 0 || b.Result.Verified != 0 || b.Result.Copied != 0 || b.Result.Status != "nothing-new" {
			t.Fatalf("%s: reads=%d result=%+v", since, reads.Load(), b.Result)
		}
	}
	// Both source size and mtime invalidate receipts, and local deletion repairs.
	c.Since = "auto"
	for _, change := range []string{"size", "mtime", "missing", "receipt", "local-size"} {
		switch change {
		case "size":
			putFile(t, root, "Dark/a.fit", "different source bytes", now.Add(-time.Hour))
		case "mtime":
			putFile(t, root, "Dark/a.fit", "different source bytes", now.Add(-2*time.Hour))
		case "missing":
			if err := os.Remove(filepath.Join(c.Destination, "Dark/a.fit")); err != nil {
				t.Fatal(err)
			}
		case "receipt":
			delete(d.State.Files, "Dark/a.fit")
		case "local-size":
			if err := os.WriteFile(filepath.Join(c.Destination, "Dark/a.fit"), []byte("damaged"), 0666); err != nil {
				t.Fatal(err)
			}
		}
		reads.Store(0)
		b := runBatch(t, d, c, source, now.Add(time.Hour))
		if reads.Load() == 0 || b.Result.Verified != 1 {
			t.Fatalf("%s: reads=%d result=%+v", change, reads.Load(), b.Result)
		}
	}
}

func TestRecheckRereadsAndRepairsSameMetadata(t *testing.T) {
	c, d, source, root, now := fixture(t)
	c.Since = "all"
	putFile(t, root, "a.fit", "good", now.Add(-time.Hour))
	var reads atomic.Int64
	source = countedReadsFs{Fs: source, reads: &reads}
	runBatch(t, d, c, source, now)
	c.Recheck = true
	reads.Store(0)
	b := runBatch(t, d, c, source, now)
	if b.Result.Verified != 1 || b.Result.Copied != 0 || reads.Load() == 0 {
		t.Fatalf("recheck reads=%d result=%+v", reads.Load(), b.Result)
	}
	putFile(t, c.Destination, "a.fit", "evil", now.Add(-time.Hour))
	c.Recheck = false
	reads.Store(0)
	b = runBatch(t, d, c, source, now)
	if reads.Load() != 0 || b.Result.Copied != 0 {
		t.Fatal("identical metadata forced a reread")
	}
	c.Recheck = true
	reads.Store(0)
	b = runBatch(t, d, c, source, now)
	if reads.Load() < 2 || b.Result.Copied != 1 {
		t.Fatalf("repair reads=%d result=%+v", reads.Load(), b.Result)
	}
}

func TestRecheckDetectsSourceChangesWithSameMetadata(t *testing.T) {
	c, d, source, root, now := fixture(t)
	c.Since = "all"
	putFile(t, root, "a.fit", "good", now.Add(-time.Hour))
	runBatch(t, d, c, source, now)
	old := d.State.Files["a.fit"].SHA256
	putFile(t, root, "a.fit", "next", now.Add(-time.Hour))
	c.Recheck = true
	b := runBatch(t, d, c, source, now)
	if b.Result.Copied != 1 || old == d.State.Files["a.fit"].SHA256 {
		t.Fatalf("same-metadata source change lost: %+v", b.Result)
	}
}

func TestRepeatReusedLocalTimestampDoesNotForceReads(t *testing.T) {
	c, d, source, root, now := fixture(t)
	c.Since = "all"
	putFile(t, root, "a.fit", "same bytes", now.Add(-time.Hour))
	putFile(t, c.Destination, "a.fit", "same bytes", now.Add(-2*time.Hour))
	var reads atomic.Int64
	source = countedReadsFs{Fs: source, reads: &reads}
	first := runBatch(t, d, c, source, now)
	if first.Result.Copied != 0 || first.Result.Verified != 1 {
		t.Fatalf("existing bytes not verified/reused: %+v", first.Result)
	}
	// Receipts record remote mtime. An imported archive may have another local
	// timestamp; this is not a change in the receipted remote object's metadata.
	reads.Store(0)
	second := runBatch(t, d, c, source, now)
	if reads.Load() != 0 || second.Result.Verified != 0 {
		t.Fatalf("reused local timestamp caused repeat reads: reads=%d result=%+v", reads.Load(), second.Result)
	}
}

// Count positive data bytes returned by the local fake or actual Samba reader.
// Opens alone cannot prove that a transfer did not read the same bytes twice.
type countedReadCloser struct {
	io.ReadCloser
	bytes *atomic.Int64
}

func (r countedReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.bytes.Add(int64(n))
	return n, err
}
func TestDefaultSourceBytesOnce(t *testing.T) {
	c, d, src, root, now := fixture(t)
	data := "independent literal transport fixture\n"
	putFile(t, root, "a.fit", data, now.Add(-time.Hour))
	var opens, bytes atomic.Int64
	b := runBatch(t, d, c, countedReadsFs{Fs: src, reads: &opens, bytes: &bytes}, now)
	if b.Result.Copied != 1 || b.Result.Verified != 1 || bytes.Load() != int64(len(data)) {
		t.Fatalf("copied=%d checked=%d source bytes=%d want=%d", b.Result.Copied, b.Result.Verified, bytes.Load(), len(data))
	}
}
