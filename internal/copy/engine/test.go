package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/remote"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// dial opens [Test]'s session. Tests replace it with the io/fs fake.
var dial = func(ctx context.Context, endpoint string, log *slog.Logger) (remote.Session, error) {
	return remote.Dial(ctx, endpoint, log)
}

// Test checks that the share at endpoint can be read: it dials, mounts
// the share and lists its root, whose entries it returns. log is the
// session's, as for remote.Dial.
func Test(ctx context.Context, endpoint string, log *slog.Logger) ([]remote.Entry, error) {
	sess, err := dial(ctx, endpoint, log)
	if err != nil {
		return nil, diagnostic.At("connect", fmt.Errorf("connect to %s: %w", endpoint, err))
	}
	defer sess.Close()
	entries, err := sess.ReadDir(ctx, ".")
	if err != nil {
		return nil, fmt.Errorf("list the share: %w", err)
	}
	return entries, nil
}
