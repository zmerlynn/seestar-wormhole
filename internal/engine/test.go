package engine

import (
	"context"
	"fmt"

	"github.com/zmerlynn/seestar-copy/internal/remote"
)

// dial opens [Test]'s session. Tests replace it with the io/fs fake.
var dial = func(ctx context.Context, endpoint string) (remote.Session, error) {
	return remote.Dial(ctx, endpoint)
}

// Test checks that the share at endpoint can be read: it dials, mounts
// the share and lists its root, whose entries it returns.
func Test(ctx context.Context, endpoint string) ([]remote.Entry, error) {
	sess, err := dial(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", endpoint, err)
	}
	defer sess.Close()
	entries, err := sess.ReadDir(ctx, ".")
	if err != nil {
		return nil, fmt.Errorf("list the share: %w", err)
	}
	return entries, nil
}
