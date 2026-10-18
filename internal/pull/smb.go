package pull

import (
	"context"
	"net"

	"github.com/rclone/rclone/backend/smb"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/lib/encoder"
)

type shareRootKey struct{}

// OpenSMB binds rclone to the supplied direct or ambient-proxy stream.
// Configure selects the share root when explicit directories are requested.
// Names are preserved exactly so portable-path preflight can refuse instead of renaming.
// Guest access and share availability are observed, not a complete Samba configuration contract:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/media/smb.md#protocol-and-access
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/media/smb.md#shares
func OpenSMB(ctx context.Context, dial func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
	ctx = smb.WithDialContext(ctx, dial)
	root := sourceRoot
	if explicit, _ := ctx.Value(shareRootKey{}).(bool); explicit {
		root = shareRoot
	}
	src, err := smb.NewFs(ctx, "seestar", root, configmap.Simple{
		"host": "device.invalid", "port": "445", "user": "guest", "pass": "",
		"idle_timeout": "0", "encoding": encoder.EncodeZero.String(),
	})
	return ctx, src, err
}
