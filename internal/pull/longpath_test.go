package pull

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/operations"
)

func TestLocalNestedLongPaths(t *testing.T) {
	root := t.TempDir()
	dest := t.TempDir()
	name := strings.Repeat("nested-folder-012345678901234567890123456789/", 9) + "frame.fit"
	putFile(t, root, name, "long path literal fixture", time.Now().Add(-time.Hour))
	ctx := Configure(context.Background(), DefaultConfig())
	src, err := local.NewFs(ctx, "source", root, configmap.Simple{})
	if err != nil {
		t.Fatal(err)
	}
	dst, err := local.NewFs(ctx, "dest", dest, configmap.Simple{})
	if err != nil {
		t.Fatal(err)
	}
	o, err := src.NewObject(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := operations.Copy(ctx, dst, nil, name, o)
	if err != nil {
		t.Fatal(err)
	}
	same, err := operations.CheckIdenticalDownload(ctx, o, copied)
	if err != nil || !same {
		t.Fatalf("long path verify=%t err=%v", same, err)
	}
	if len(filepath.Join(dest, filepath.FromSlash(name))) <= 400 {
		t.Fatal("fixture path was too short")
	}
}
