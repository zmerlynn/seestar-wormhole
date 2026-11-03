//go:build smbintegration

package remote

import (
	"context"
	"path"
	"time"

	"github.com/zmerlynn/seestar-copy/internal/readonly"
)

// DialScratch is DialRemover for the Samba fixture's guest-writable
// scratch share, which other packages' integration tests delete from.
// Only the smbintegration build has it.
func DialScratch(ctx context.Context, endpoint string) (*SMBRemover, error) {
	s, err := dialShare(ctx, endpoint, newDialer(), "scratch")
	if err != nil {
		return nil, err
	}
	return &SMBRemover{s}, nil
}

// FillScratch replaces the scratch folder dir with files, a map from each
// file's path on the share to its contents, modified in 2020 so that a
// copy finds them settled.
func FillScratch(ctx context.Context, r *SMBRemover, dir string, files map[string]string) error {
	readonly.Check("remote.FillScratch")
	// A folder left read-only by an earlier run would refuse RemoveAll.
	_ = r.share.Chmod(ctx, dir, 0o777)
	if err := r.share.RemoveAll(ctx, dir); err != nil {
		return err
	}
	for p, data := range files {
		if err := PutScratch(ctx, r, p, data); err != nil {
			return err
		}
	}
	return nil
}

// PutScratch writes the scratch share's file p with data, and its
// folders, modified in 2020.
func PutScratch(ctx context.Context, r *SMBRemover, p, data string) error {
	readonly.Check("remote.PutScratch")
	settled := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := r.share.MkdirAll(ctx, path.Dir(p), 0o755); err != nil {
		return err
	}
	if err := r.share.WriteFile(ctx, p, []byte(data), 0o644); err != nil {
		return err
	}
	return r.share.Chtimes(ctx, p, settled, settled)
}
