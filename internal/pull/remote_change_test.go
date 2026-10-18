package pull

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

type changingReadCloser struct {
	io.ReadCloser
	after func()
}

func (r changingReadCloser) Close() error { err := r.ReadCloser.Close(); r.after(); return err }

func TestRemoteChangesRemainUnresolved(t *testing.T) {
	c, d, src, root, now := fixture(t)
	putFile(t, root, "Dark/a.fit", "initial bytes", now.Add(-time.Hour))
	changed := false
	source := injectedFs{src, func(_ context.Context, o fs.Object) fs.Object {
		return injectedObject{o, func(ctx context.Context, opts ...fs.OpenOption) (io.ReadCloser, error) {
			reader, err := o.Open(ctx, opts...)
			if err != nil {
				return nil, err
			}
			return changingReadCloser{reader, func() {
				if !changed {
					changed = true
					putFile(t, root, "Dark/a.fit", "different bytes and size", now.Add(-time.Minute))
				}
			}}, nil
		}}
	}}
	if _, err := d.Run(context.Background(), c, now, source); err == nil {
		t.Fatal("changing remote was accepted")
	} else {
		_, reason := diagnostic.Fields(err, "pull")
		if reason != "remote_changed" {
			t.Fatalf("reason=%s want remote_changed", reason)
		}
	}
	if len(d.State.Files) != 0 || len(d.State.Selections) != 0 {
		t.Fatal("changing remote was receipted/checkpointed")
	}
}
