package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type shortEOFFs struct {
	fs.Fs
	open func() io.ReadCloser
}

func (f shortEOFFs) NewObject(ctx context.Context, name string) (fs.Object, error) {
	o, err := f.Fs.NewObject(ctx, name)
	if err != nil {
		return nil, err
	}
	return shortEOFObject{o, f.open}, nil
}

type shortEOFObject struct {
	fs.Object
	open func() io.ReadCloser
}

func (o shortEOFObject) Open(context.Context, ...fs.OpenOption) (io.ReadCloser, error) {
	return o.open(), nil
}

type shortEOFReader struct {
	io.Reader
	onRead, onClose func()
	closeErr        error
}

func (r shortEOFReader) Read(p []byte) (int, error) {
	if r.onRead != nil {
		r.onRead()
		return 0, io.EOF
	}
	return r.Reader.Read(p)
}

func (r shortEOFReader) Close() error {
	if r.onClose != nil {
		r.onClose()
	}
	return r.closeErr
}

// Exercise the production copy, worker draining, release and exit projection.
// Source metadata remains intact: only the stream ends before the listed size.
func TestPullInterruptedEOFExit(t *testing.T) {
	for _, test := range []struct {
		name, cancelAt, reason string
		closeErr, releaseErr   error
		code                   int
	}{
		{name: "cancel-on-read", cancelAt: "read", reason: "canceled", code: 130},
		{name: "cancel-on-close", cancelAt: "close", reason: "canceled", code: 130},
		{name: "short-without-cancel", reason: "verify_mismatch", code: 1},
		{name: "joined-copy-error", cancelAt: "close", closeErr: errors.Join(context.Canceled, errors.ErrUnsupported), reason: "verify_mismatch", code: 1},
		{name: "release-failure", cancelAt: "read", releaseErr: session.ErrStop, reason: "release_failed", code: 1},
		{name: "joined-release-error", cancelAt: "close", releaseErr: errors.Join(context.Canceled, errors.ErrUnsupported), reason: "operation_failed", code: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			root, dest := t.TempDir(), t.TempDir()
			filename := filepath.Join(root, "a.fit")
			if err := os.WriteFile(filename, []byte("complete source remains intact"), 0644); err != nil {
				t.Fatal(err)
			}
			mtime := time.Now().Add(-time.Hour)
			if err := os.Chtimes(filename, mtime, mtime); err != nil {
				t.Fatal(err)
			}
			source, err := local.NewFs(ctx, "fixture-source", root, configmap.Simple{})
			if err != nil {
				t.Fatal(err)
			}
			s := &pullSessionFixture{closeErr: test.releaseErr}
			var out, logs bytes.Buffer
			err = runPullWithSource(ctx, []string{"pull", "--settle=0", dest}, &out, &logs, keylog.Open,
				func(context.Context, proxy.Config) (commandSession, error) { return s, nil },
				func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
					return ctx, shortEOFFs{source, func() io.ReadCloser {
						r := shortEOFReader{Reader: strings.NewReader("short"), closeErr: test.closeErr}
						switch test.cancelAt {
						case "read":
							r.onRead = cancel
						case "close":
							r.onClose = cancel
						}
						return r
					}}, nil
				})
			status := "error"
			if test.code == 130 {
				status = "canceled"
			}
			if got := pullCode(err); got != test.code || !strings.Contains(out.String(), "status="+status+" ") || !strings.Contains(out.String(), " reason="+test.reason+" ") {
				t.Fatalf("code=%d want=%d status=%s reason=%s stdout=%s", got, test.code, status, test.reason, &out)
			}
			if s.closed != 1 {
				t.Fatalf("releases=%d want=1", s.closed)
			}
			for _, cause := range []error{test.closeErr, test.releaseErr} {
				if cause != nil && !errors.Is(err, cause) {
					t.Fatalf("lost independent cause %v: %v", cause, err)
				}
			}
			d, err := pull.OpenDestination(dest)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if len(d.State.Files) != 0 || len(d.State.Selections) != 0 {
				t.Fatalf("failed copy persisted receipts/checkpoints: %+v", d.State)
			}
			if _, err := os.Stat(filepath.Join(dest, "a.fit")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed copy promoted archive: %v", err)
			}
		})
	}
}
