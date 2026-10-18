// Package pull implements restartable, verified MyWorks downloads using rclone.
// It owns bounded destination state; the command owns acquisition and release.
//
// Wire reference: https://github.com/zmerlynn/seestar-spec/blob/v1/spec/media/smb.md
package pull
