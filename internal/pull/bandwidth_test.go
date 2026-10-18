package pull

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/accounting"
)

func TestBandwidthWaitCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		accounting.TokenBucket.SetBwLimit(fs.BwPair{Rx: 1, Tx: 1})
		defer accounting.TokenBucket.SetBwLimit(fs.BwPair{})
		transfer := accounting.Stats(ctx).NewTransferRemoteSize("fixture", 1024, nil, nil)
		reader := transfer.Account(ctx, io.NopCloser(bytes.NewReader(make([]byte, 1024))))
		done := make(chan error, 1)
		go func() { _, err := reader.Read(make([]byte, 1024)); transfer.Done(ctx, err); done <- err }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("wait error=%v want cancellation", err)
			}
		default:
			t.Error("bandwidth wait ignored cancellation")
			<-done
		}
	})
}

func TestBandwidthDeadlineRejectsUnboundedTransfer(t *testing.T) {
	for _, perFile := range []bool{false, true} {
		for _, method := range []string{"Read", "ReadAt", "WriteTo", "AccountRead"} {
			t.Run(fmt.Sprintf("per-file=%t/%s", perFile, method), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					ctx, ci := fs.AddConfig(ctx)
					if perFile {
						if err := ci.BwLimitFile.Set("1Ki"); err != nil {
							t.Fatal(err)
						}
					} else {
						accounting.TokenBucket.SetBwLimit(fs.BwPair{Rx: 1024, Tx: 1024})
						defer accounting.TokenBucket.SetBwLimit(fs.BwPair{})
					}
					const size = 131072
					transfer := accounting.Stats(ctx).NewTransferRemoteSize("fixture", size, nil, nil)
					input := &bandwidthReader{Reader: bytes.NewReader(make([]byte, size))}
					reader := transfer.Account(ctx, input)
					defer reader.Close()
					start := time.Now()
					var err error
					var output bytes.Buffer
					switch method {
					case "Read":
						_, err = io.CopyBuffer(&output, struct{ io.Reader }{reader}, make([]byte, 4096))
					case "ReadAt":
						r, e := reader.WithReaderAt()
						if e != nil {
							t.Fatal(e)
						}
						_, err = r.ReadAt(make([]byte, size), 0)
					case "WriteTo":
						_, err = reader.WriteTo(&output)
					case "AccountRead":
						err = reader.AccountRead(size)
					}
					transfer.Done(ctx, err)
					if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second || output.Len() == size {
						t.Fatalf("rejected wait bypassed: err=%v elapsed=%v read=%d written=%d", err, time.Since(start), input.bytes, output.Len())
					}
					// Accounting happens after a bounded input chunk; failure must prevent
					// unlimited subsequent reads, even if the deadline has not yet fired.
					if method == "Read" && input.bytes > 8192 {
						t.Fatalf("unbounded input bytes: %d", input.bytes)
					}
				})
			})
		}
	}
}

type bandwidthReader struct {
	*bytes.Reader
	bytes int
}

func (r *bandwidthReader) Read(p []byte) (int, error) {
	n, e := r.Reader.Read(p)
	r.bytes += n
	return n, e
}
func (r *bandwidthReader) Close() error { return nil }
