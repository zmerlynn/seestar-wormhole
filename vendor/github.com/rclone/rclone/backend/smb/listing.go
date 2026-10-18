package smb

import (
	"context"
	"os"
)

type readDirKey struct{}

// WithReadDir supplies a context-scoped consumer of incremental directory reads.
// The SMB backend owns opening and closing the directory; the consumer must not
// retain or call readdir after returning. SMB decoding remains in go-smb2.
func WithReadDir(ctx context.Context, readDir func(context.Context, string, func(int) ([]os.FileInfo, error)) ([]os.FileInfo, error)) context.Context {
	return context.WithValue(ctx, readDirKey{}, readDir)
}

// ReadDir dispatches directory reads to the context consumer, if installed.
// Without one, it retains the backend's ordinary whole-directory behavior.
// Keeping dispatch separate from opening permits offline synthetic enumerators.
func ReadDir(ctx context.Context, dir string, readdir func(int) ([]os.FileInfo, error)) ([]os.FileInfo, error) {
	if readDir, ok := ctx.Value(readDirKey{}).(func(context.Context, string, func(int) ([]os.FileInfo, error)) ([]os.FileInfo, error)); ok {
		return readDir(ctx, dir, readdir)
	}
	return readdir(-1)
}
