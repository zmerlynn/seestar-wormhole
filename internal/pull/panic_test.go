package pull

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/hash"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

type panickingObject struct{}

func (panickingObject) Fs() fs.Info                       { panic("private metadata") }
func (panickingObject) String() string                    { panic("private metadata") }
func (panickingObject) Remote() string                    { panic("private metadata") }
func (panickingObject) ModTime(context.Context) time.Time { panic("private metadata") }
func (panickingObject) Size() int64                       { panic("private metadata") }
func (panickingObject) Storable() bool                    { panic("private metadata") }
func (panickingObject) Hash(context.Context, hash.Type) (string, error) {
	panic("private metadata")
}
func (panickingObject) SetModTime(context.Context, time.Time) error { panic("private metadata") }
func (panickingObject) Open(context.Context, ...fs.OpenOption) (io.ReadCloser, error) {
	panic("private metadata")
}
func (panickingObject) Update(context.Context, io.Reader, fs.ObjectInfo, ...fs.OpenOption) error {
	panic("private metadata")
}
func (panickingObject) Remove(context.Context) error { panic("private metadata") }

type panickingInfo struct{}

func (panickingInfo) Name() string             { panic("private metadata") }
func (panickingInfo) Root() string             { panic("private metadata") }
func (panickingInfo) String() string           { panic("private metadata") }
func (panickingInfo) Precision() time.Duration { panic("private metadata") }
func (panickingInfo) Hashes() hash.Set         { panic("private metadata") }
func (panickingInfo) Features() *fs.Features   { panic("private metadata") }

// Interface growth must require a new explicit guarded implementation. A method
// promoted from an embedded backend would otherwise satisfy fs.Object silently.
func TestGuardedMethodSets(t *testing.T) {
	for _, pair := range []struct {
		wrapper, contract reflect.Type
	}{
		{reflect.TypeFor[guardedObject](), reflect.TypeFor[fs.Object]()},
		{reflect.TypeFor[guardedInfo](), reflect.TypeFor[fs.Info]()},
	} {
		if !pair.wrapper.Implements(pair.contract) || pair.wrapper.NumMethod() != pair.contract.NumMethod() {
			t.Fatalf("%v method set differs from %v", pair.wrapper, pair.contract)
		}
		for i := range pair.wrapper.NumField() {
			if pair.wrapper.Field(i).Anonymous {
				t.Fatalf("%v embeds %v: backend methods must not be promoted", pair.wrapper, pair.wrapper.Field(i).Type)
			}
		}
		for i := range pair.contract.NumMethod() {
			name := pair.contract.Method(i).Name
			if _, ok := pair.wrapper.MethodByName(name); !ok {
				t.Errorf("%v lacks explicit %s", pair.wrapper, name)
			}
		}
	}
}

func TestGuardedBackendMethodsCancelBeforeReturning(t *testing.T) {
	for _, contract := range []reflect.Type{reflect.TypeFor[fs.Object](), reflect.TypeFor[fs.Info]()} {
		for i := range contract.NumMethod() {
			name := contract.Method(i).Name
			t.Run(contract.Name()+"/"+name, func(t *testing.T) {
				ctx, cancel := context.WithCancelCause(t.Context())
				defer cancel(nil)
				aborted := false
				boundary := panicBoundary{cancel: cancel, abort: func() { aborted = true }}
				var wrapper any = guardedInfo{info: panickingInfo{}, boundary: boundary}
				if contract == reflect.TypeFor[fs.Object]() {
					wrapper = guardedObject{object: panickingObject{}, name: "captured", boundary: boundary}
				}
				method := reflect.ValueOf(wrapper).MethodByName(name)
				args := make([]reflect.Value, method.Type().NumIn())
				for j := range args {
					args[j] = reflect.Zero(method.Type().In(j))
					if method.Type().In(j) == reflect.TypeFor[context.Context]() {
						args[j] = reflect.ValueOf(ctx)
					}
				}
				var results []reflect.Value
				if method.Type().IsVariadic() {
					results = method.CallSlice(args)
				} else {
					results = method.Call(args)
				}
				if contract == reflect.TypeFor[fs.Object]() && (name == "Remote" || name == "String") {
					if results[0].String() != "captured" || context.Cause(ctx) != nil || aborted {
						t.Fatal("captured name called backend")
					}
					return
				}
				if !errors.Is(context.Cause(ctx), diagnostic.ErrPullPanic) || !aborted {
					t.Fatalf("returned before cancellation: cause=%v aborted=%t", context.Cause(ctx), aborted)
				}
				for _, result := range results {
					if result.Type() == reflect.TypeFor[error]() && !errors.Is(result.Interface().(error), diagnostic.ErrPullPanic) {
						t.Errorf("unclassified error: %v", result.Interface())
					}
				}
				if name == "Size" && results[0].Int() != -1 {
					t.Error("failed Size must return unknown size")
				}
				if name == "Fs" {
					// A failed Fs still supplies a safe Info, rather than letting a
					// subsequent metadata call panic on a nil interface.
					info := results[0].Interface().(fs.Info)
					if info.Name() != "" || info.Features() == nil {
						t.Error("failed Fs returned unsafe metadata")
					}
				}
			})
		}
	}
}

type cancelThenPanicObject struct {
	fs.Object
	cancel context.CancelFunc
}

func (o cancelThenPanicObject) Size() int64 {
	o.cancel()
	panic("private metadata")
}

func TestGuardedMetadataPanicWinsEarlierCancellation(t *testing.T) {
	c, d, source, root, now := fixture(t)
	putFile(t, root, "private.fit", "fixture", now.Add(-time.Hour))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	src := listingFs{Fs: source, list: func(ctx context.Context, dir string) (fs.DirEntries, error) {
		entries, err := source.List(ctx, dir)
		if err != nil {
			return nil, err
		}
		return fs.DirEntries{cancelThenPanicObject{Object: entries[0].(fs.Object), cancel: cancel}}, nil
	}}
	_, err := d.Run(ctx, c, now, src)
	if !errors.Is(err, diagnostic.ErrPullPanic) || !errors.Is(err, context.Canceled) {
		t.Fatalf("lost metadata panic behind cancellation: %v", err)
	}
	requireReason(t, err, "operation_panic")
	if len(d.State.Files) != 0 || len(d.State.Selections) != 0 {
		t.Fatal("metadata panic changed persisted state")
	}
}
