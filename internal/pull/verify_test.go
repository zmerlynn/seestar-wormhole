package pull

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
)

func TestVerifySourceBytesTwice(t *testing.T) {
	c, d, src, root, now := fixture(t)
	c.Verify = true
	data := "literal transport fixture\n"
	putFile(t, root, "a.fit", data, now.Add(-time.Hour))
	var opens, bytes atomic.Int64
	b := runBatch(t, d, c, countedReadsFs{Fs: src, reads: &opens, bytes: &bytes}, now)
	r := d.State.Files["a.fit"]
	if b.Result.Copied != 1 || bytes.Load() != 2*int64(len(data)) || r.Check != checkReread || r.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(data))) {
		t.Fatalf("result=%+v source bytes=%d receipt=%+v", b.Result, bytes.Load(), r)
	}
}

func TestVerifyReceiptUpgrade(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(fmt.Sprint(repair), func(t *testing.T) {
			c, d, src, root, now := fixture(t)
			data := "good"
			putFile(t, root, "a.fit", data, now.Add(-time.Hour))
			runBatch(t, d, c, src, now)
			original := d.State.Files["a.fit"]
			if original.Check != checkSize {
				t.Fatalf("default receipt=%+v", original)
			}
			// A separate invocation has a size receipt below an auto checkpoint.
			d.Close()
			var err error
			d, err = OpenDestination(c.Destination)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if repair {
				putFile(t, c.Destination, "a.fit", "evil", now.Add(-time.Hour))
			}
			c.Overlap = 0
			c.Verify = true
			var opens, bytes atomic.Int64
			wrapped := countedReadsFs{Fs: src, reads: &opens, bytes: &bytes}
			b := runBatch(t, d, c, wrapped, now.Add(time.Hour))
			wantCopied, wantReads := 0, int64(len(data))
			if repair {
				wantCopied, wantReads = 1, 3*int64(len(data))
			}
			r := d.State.Files["a.fit"]
			if b.Result.Copied != wantCopied || b.Result.Verified != 1 || bytes.Load() != wantReads || r.Check != checkReread || r.Source != original.Source || r.Generation != original.Generation || r.SHA256 != original.SHA256 {
				t.Fatalf("upgrade result=%+v bytes=%d want=%d receipt=%+v", b.Result, bytes.Load(), wantReads, r)
			}
			bytes.Store(0)
			b = runBatch(t, d, c, wrapped, now.Add(2*time.Hour))
			if bytes.Load() != 0 || b.Result.Verified != 0 {
				t.Fatalf("reread receipt repeated: bytes=%d %+v", bytes.Load(), b.Result)
			}
			c.Since = "all"
			c.Recheck = true
			bytes.Store(0)
			b = runBatch(t, d, c, wrapped, now.Add(3*time.Hour))
			if bytes.Load() != int64(len(data)) || b.Result.Verified != 1 || b.Result.Copied != 0 {
				t.Fatalf("recheck bytes=%d %+v", bytes.Load(), b.Result)
			}
		})
	}
}

// A faulting writer consumes a source stream and returns cached metadata for a
// full-sized file. It either truncates the actual file after Put or pads a short
// source stream. These independent faults separate written length from bytes
// read, preventing rclone's own metadata size check from masking either guard.
type faultingDestination struct {
	fs.Fs
	truncate bool
}

func (f faultingDestination) Put(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (fs.Object, error) {
	data, err := io.ReadAll(in)
	if err != nil {
		return nil, err
	}
	data = append(data, make([]byte, max(0, int(src.Size())-len(data)))...)
	o, err := f.Fs.Put(ctx, strings.NewReader(string(data)), src, options...)
	if err != nil {
		return nil, err
	}
	if f.truncate {
		if err = os.Truncate(filepath.Join(f.Root(), filepath.FromSlash(src.Remote())), src.Size()-1); err != nil {
			return nil, err
		}
	}
	return o, nil
}

func TestCopyLengthGuards(t *testing.T) {
	for _, fault := range []string{"written-length", "source-byte-count"} {
		t.Run(fault, func(t *testing.T) {
			c, d, src, root, now := fixture(t)
			putFile(t, root, "a.fit", "independent length fixture", now.Add(-time.Hour))
			// Preserve an existing archive whose different length forces a copy.
			putFile(t, c.Destination, "a.fit", "old archive", now.Add(-time.Hour))
			if fault == "source-byte-count" {
				src = injectedFs{src, func(_ context.Context, o fs.Object) fs.Object {
					return injectedObject{o, func(context.Context, ...fs.OpenOption) (io.ReadCloser, error) {
						return io.NopCloser(strings.NewReader("short")), nil
					}}
				}}
			}
			ctx := Configure(context.Background(), c)
			objects, _, err := inventoryDirectories(ctx, src, nil, []string{""})
			if err != nil {
				t.Fatal(err)
			}
			dst, err := local.NewFs(ctx, "faulting-destination", c.Destination, configmap.Simple{})
			if err != nil {
				t.Fatal(err)
			}
			b := &Batch{destination: d, config: c, Result: Result{Status: "error"}}
			err = b.transfer(ctx, src, faultingDestination{dst, fault == "written-length"}, objects)
			requireReason(t, err, "verify_mismatch")
			if b.Result.Copied != 0 || b.Result.Verified != 0 || len(d.State.Files) != 0 || len(d.State.Selections) != 0 {
				t.Fatalf("mismatched copy receipted: %+v %+v", b.Result, d.State)
			}
			data, err := os.ReadFile(filepath.Join(c.Destination, "a.fit"))
			if err != nil || string(data) != "old archive" {
				t.Fatalf("prior archive=%q err=%v", data, err)
			}
			partials, err := filepath.Glob(filepath.Join(c.Destination, ".seestar-part-*"))
			if err != nil || len(partials) != 0 {
				t.Fatalf("partials=%v err=%v", partials, err)
			}
		})
	}
}

func TestVersionThreeReceiptsMigrateAsReread(t *testing.T) {
	c, d, src, root, now := fixture(t)
	putFile(t, root, "a.fit", "good", now.Add(-time.Hour))
	c.Verify = true
	runBatch(t, d, c, src, now)
	original := d.State.Files["a.fit"]
	r := original
	r.Check = "" // v3 had no check field and always compared downloads
	d.State.Files["a.fit"] = r
	generation := d.State.Generation
	checkpoint := d.State.Selections[selectionKey(nil)].Checkpoint
	d.State.Version = 3
	if err := d.save(); err != nil {
		t.Fatal(err)
	}
	d.Close()
	var err error
	d, err = OpenDestination(c.Destination)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.State.Version != 4 || d.State.Files["a.fit"] != original || d.State.Generation != generation || d.State.Selections[selectionKey(nil)].Checkpoint != checkpoint {
		t.Fatalf("migration=%+v", d.State)
	}
	var opens, bytes atomic.Int64
	runBatch(t, d, c, countedReadsFs{Fs: src, reads: &opens, bytes: &bytes}, now.Add(time.Hour))
	if bytes.Load() != 0 {
		t.Fatalf("migrated reread receipt read %d bytes", bytes.Load())
	}
}

func TestReceiptCheckRefusal(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, check := range []string{"", "unknown"} {
			t.Run(fmt.Sprintf("explicit=%t/check=%s", explicit, check), func(t *testing.T) {
				c, d, src, root, now := fixture(t)
				name := "a.fit"
				if explicit {
					d.State.Source = ""
					c.Dirs = []string{"MyWorks"}
					if err := d.BindLayout(c.Dirs); err != nil {
						t.Fatal(err)
					}
					if err := d.BindSource("test-source"); err != nil {
						t.Fatal(err)
					}
					name = "MyWorks/a.fit"
				}
				putFile(t, root, name, "good", now.Add(-time.Hour))
				runBatch(t, d, c, src, now)
				r := d.State.Files[name]
				r.Check = check
				d.State.Files[name] = r
				if err := d.save(); err != nil {
					t.Fatal(err)
				}
				d.Close()
				opened, err := OpenDestination(c.Destination)
				if opened != nil {
					opened.Close()
				}
				requireReason(t, err, "state_invalid")
			})
		}
	}
}

func TestShortSourceLengthFailure(t *testing.T) {
	c, d, src, root, now := fixture(t)
	putFile(t, root, "a.fit", "listed full length", now.Add(-time.Hour))
	src = injectedFs{src, func(_ context.Context, o fs.Object) fs.Object {
		return injectedObject{o, func(context.Context, ...fs.OpenOption) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("short")), nil
		}}
	}}
	_, err := d.Run(context.Background(), c, now, src)
	requireReason(t, err, "verify_mismatch")
	if len(d.State.Files) != 0 {
		t.Fatal("short source was receipted")
	}
}

func TestCopyByteCountAcrossReaderReopen(t *testing.T) {
	c, d, src, root, now := fixture(t)
	data := "literal retry fixture"
	putFile(t, root, "a.fit", data, now.Add(-time.Hour))
	var opens int
	src = injectedFs{src, func(_ context.Context, o fs.Object) fs.Object {
		return injectedObject{o, func(ctx context.Context, opts ...fs.OpenOption) (io.ReadCloser, error) {
			opens++
			if opens == 1 {
				return &interruptedCopyReader{data: []byte(data[:4])}, nil
			}
			return o.Open(ctx, opts...)
		}}
	}}
	b := runBatch(t, d, c, src, now)
	if opens != 2 || b.Result.Copied != 1 {
		t.Fatalf("reopen opens=%d result=%+v", opens, b.Result)
	}
	got, err := os.ReadFile(filepath.Join(c.Destination, "a.fit"))
	if err != nil || string(got) != data {
		t.Fatalf("reopened bytes=%q err=%v", got, err)
	}
}

type interruptedCopyReader struct{ data []byte }

func (r *interruptedCopyReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, io.ErrUnexpectedEOF
}
func (*interruptedCopyReader) Close() error { return nil }
