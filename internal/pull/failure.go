package pull

import (
	"errors"
	"syscall"

	"github.com/rclone/rclone/lib/file"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/oserror"
)

func failure(reason diagnostic.PullReason) error { return &diagnostic.PullError{Reason: reason} }

// Failure preserves typed backend errors while attaching a safe pull class.
// In particular rclone preallocation uses its own disk-full sentinel rather
// than ENOSPC. Neither backend error text nor filesystem paths are inspected.
func Failure(err error) error {
	if errors.Is(err, file.ErrDiskFull) || oserror.Is(err, syscall.ENOSPC) {
		return &diagnostic.PullError{Reason: diagnostic.DiskFull, Cause: err}
	}
	return err
}
