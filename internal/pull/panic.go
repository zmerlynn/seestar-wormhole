package pull

import (
	"context"
	"io"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// Operation runs a pull callback at an owned goroutine boundary and joins it.
// Only explicitly supplied operations are contained; the calling goroutine's
// programming errors still panic. Recovered values are never retained/formatted.
func Operation(logger *slog.Logger, fn func() error) error {
	done := make(chan error, 1)
	go func() {
		var err error
		defer func() { done <- err }()
		defer panicBoundary{logger: logger}.recover(&err)
		err = fn()
	}()
	return <-done
}

type panicBoundary struct {
	logger *slog.Logger
	cancel context.CancelCauseFunc
	abort  context.CancelFunc
}

func (p panicBoundary) failure() error {
	err := diagnostic.ErrPullPanic
	if p.cancel != nil {
		p.cancel(err)
	}
	if p.abort != nil {
		p.abort()
	}
	if p.logger != nil && p.logger.Enabled(context.Background(), slog.LevelDebug) {
		p.logger.Debug("pull operation panicked", "reason", "operation_panic", "stack", string(debug.Stack()))
	}
	return err
}

func (p panicBoundary) recover(err *error) {
	if recover() != nil {
		*err = p.failure()
	}
}

// The library walker and buffered reader own their own goroutines. Contain the
// backend callbacks they invoke, so those workers can cancel and join normally.
// SMB has no recursive listing; disabling optional ListR also keeps adapters
// from launching unguarded backend-specific recursive workers.
type guardedSource struct {
	fs.Fs
	boundary panicBoundary
}

func (f guardedSource) Features() *fs.Features {
	features := *f.Fs.Features()
	features.ListR = nil
	return &features
}

func (f guardedSource) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	defer f.boundary.recover(&err)
	entries, err = f.Fs.List(ctx, dir)
	if err != nil {
		return nil, err
	}
	guarded := make(fs.DirEntries, len(entries))
	for i, entry := range entries {
		name := entry.Remote()
		if err := validateListedRemote(dir, name); err != nil {
			return nil, err
		}
		guarded[i] = entry
		// The walker uses names and directory metadata outside its callback. Read
		// these under this boundary, then give it immutable standard entries.
		switch entry := entry.(type) {
		case fs.Object:
			guarded[i] = guardedObject{object: entry, name: name, boundary: f.boundary}
		case fs.Directory:
			guarded[i] = fs.NewDir(name, entry.ModTime(ctx)).SetSize(entry.Size()).SetItems(entry.Items()).SetID(entry.ID())
		}
	}
	return guarded, nil
}

func (f guardedSource) NewObject(ctx context.Context, name string) (result fs.Object, err error) {
	defer f.boundary.recover(&err)
	o, err := f.Fs.NewObject(ctx, name)
	if err != nil {
		return nil, err
	}
	return guardedObject{object: o, name: name, boundary: f.boundary}, nil
}

// Do not embed fs.Object: every interface method must stay behind this boundary,
// including metadata used by rclone's reopen, buffering and hash workers.
type guardedObject struct {
	object   fs.Object
	name     string
	boundary panicBoundary
}

// Metadata APIs cannot return errors. Cancel with the typed failure before
// returning an inert value, so transport reads stop before any library join.
// Destination.Run joins the cancellation cause into its result; these fallback
// values cannot turn a panic into a successful batch or checkpoint commit.
func guardedValue[T any](boundary panicBoundary, fallback T, fn func() T) (value T) {
	value = fallback
	defer func() {
		if recover() != nil {
			boundary.failure()
		}
	}()
	return fn()
}

func (o guardedObject) Remote() string { return o.name }
func (o guardedObject) String() string { return o.name }
func (o guardedObject) Size() int64 {
	return guardedValue(o.boundary, int64(-1), o.object.Size)
}
func (o guardedObject) ModTime(ctx context.Context) time.Time {
	return guardedValue(o.boundary, time.Time{}, func() time.Time { return o.object.ModTime(ctx) })
}
func (o guardedObject) Storable() bool {
	return guardedValue(o.boundary, false, o.object.Storable)
}
func (o guardedObject) Fs() fs.Info {
	info := guardedValue(o.boundary, fs.Info(nil), o.object.Fs)
	return guardedInfo{info: info, boundary: o.boundary}
}
func (o guardedObject) Hash(ctx context.Context, ty hash.Type) (sum string, err error) {
	defer o.boundary.recover(&err)
	return o.object.Hash(ctx, ty)
}
func (o guardedObject) SetModTime(ctx context.Context, t time.Time) (err error) {
	defer o.boundary.recover(&err)
	return o.object.SetModTime(ctx, t)
}
func (o guardedObject) Update(ctx context.Context, in io.Reader, src fs.ObjectInfo, options ...fs.OpenOption) (err error) {
	defer o.boundary.recover(&err)
	return o.object.Update(ctx, in, src, options...)
}
func (o guardedObject) Remove(ctx context.Context) (err error) {
	defer o.boundary.recover(&err)
	return o.object.Remove(ctx)
}
func (o guardedObject) Open(ctx context.Context, options ...fs.OpenOption) (r io.ReadCloser, err error) {
	defer o.boundary.recover(&err)
	r, err = o.object.Open(ctx, options...)
	if err != nil {
		return nil, err
	}
	return guardedReader{ReadCloser: r, boundary: o.boundary}, nil
}

// Fs returns another backend interface. Guard its metadata too rather than
// allowing callers to reach the backend through the guarded object's Fs method.
type guardedInfo struct {
	info     fs.Info
	boundary panicBoundary
}

func (f guardedInfo) Name() string {
	return guardedValue(f.boundary, "", func() string { return f.info.Name() })
}
func (f guardedInfo) Root() string {
	return guardedValue(f.boundary, "", func() string { return f.info.Root() })
}
func (f guardedInfo) String() string {
	return guardedValue(f.boundary, "", func() string { return f.info.String() })
}
func (f guardedInfo) Precision() time.Duration {
	return guardedValue(f.boundary, fs.ModTimeNotSupported, func() time.Duration { return f.info.Precision() })
}
func (f guardedInfo) Hashes() hash.Set {
	return guardedValue(f.boundary, hash.Set(0), func() hash.Set { return f.info.Hashes() })
}
func (f guardedInfo) Features() *fs.Features {
	return guardedValue(f.boundary, &fs.Features{}, func() *fs.Features { return f.info.Features() })
}

type guardedReader struct {
	io.ReadCloser
	boundary panicBoundary
}

func (r guardedReader) Read(p []byte) (n int, err error) {
	defer r.boundary.recover(&err)
	return r.ReadCloser.Read(p)
}
func (r guardedReader) Close() (err error) {
	defer r.boundary.recover(&err)
	return r.ReadCloser.Close()
}
